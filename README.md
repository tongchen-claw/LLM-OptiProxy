# LLM-OptiProxy

Anthropic `POST /v1/messages` transparent proxy with audit hooks and an initial asynchronous checkpointing path.

## Run

```sh
go run ./cmd/proxy
```

The proxy listens on `:8080` by default and forwards to `https://api.anthropic.com`.

```sh
OPTIPROXY_LISTEN_ADDR=:8080 \
OPTIPROXY_UPSTREAM_BASE_URL=https://api.anthropic.com \
go run ./cmd/proxy
```

## Checkpointing

Checkpointing is disabled by default.

```sh
OPTIPROXY_ENABLE_CHECKPOINTING=true \
OPTIPROXY_CHECKPOINT_COMPRESSOR=openai-chat \
OPTIPROXY_COMPRESSOR_BASE_URL=https://api.openai.com \
OPTIPROXY_COMPRESSOR_MODEL=your-compressor-model \
OPTIPROXY_COMPRESSOR_API_KEY=sk-... \
OPTIPROXY_KEEP_RECENT_TURNS=3 \
OPTIPROXY_MESSAGE_COUNT_THRESHOLD=10 \
go run ./cmd/proxy
```

When a local checkpoint is hit and replaces older messages in the forwarded request, the proxy writes a JSONL savings log entry if estimated input tokens were saved. By default this goes to stdout. Configure the destination with:

```sh
OPTIPROXY_SAVINGS_LOG_PATH=data/savings.jsonl ./bin/llm-optiproxy
```

Use `-` to write savings entries to stdout.

When running the compiled binary, the last environment variable line must not end with a trailing `\` unless the binary command is on the next physical line:

```sh
OPTIPROXY_LISTEN_ADDR=:8080 \
OPTIPROXY_UPSTREAM_BASE_URL=https://api.openai-proxy.org/anthropic \
OPTIPROXY_ENABLE_CHECKPOINTING=true \
OPTIPROXY_CHECKPOINT_COMPRESSOR=openai-chat \
OPTIPROXY_SAVINGS_LOG_PATH=data/savings.jsonl \
OPTIPROXY_COMPRESSOR_BASE_URL=https://api.openai-proxy.org \
OPTIPROXY_COMPRESSOR_MODEL=your-compressor-model \
OPTIPROXY_COMPRESSOR_API_KEY="$COMPRESSOR_API_KEY" \
./bin/llm-optiproxy
```

The current implementation:

- keeps the last `OPTIPROXY_KEEP_RECENT_TURNS` turns uncompressed
- builds checkpoints asynchronously after the upstream response is relayed
- deduplicates in-flight checkpoint builds by prefix, coverage end, keep-turn count, and compressor mode
- uses an in-memory store for checkpoint state
- supports `openai-chat` for OpenAI Chat Completions compatible compressor APIs
- also supports `local-extractive` for local development without a remote compressor

`OPTIPROXY_COMPRESSOR_BASE_URL` can be either the service root, a `/v1` API root, or a full `/chat/completions` endpoint.

## Audit

Audit logging is enabled by default and writes metadata-only JSONL records to `data/audit.jsonl`.
Token usage and estimated savings are also persisted to SQLite at `data/usage.sqlite`.

```sh
OPTIPROXY_AUDIT_LOG_PATH=- go run ./cmd/proxy
```

Use `-` to write audit records to stdout.

Configure the SQLite path with:

```sh
OPTIPROXY_USAGE_SQLITE_PATH=data/usage.sqlite ./bin/llm-optiproxy
```

Query cumulative token savings:

```sh
sqlite3 data/usage.sqlite '
SELECT
  request_count,
  checkpoint_hit_count,
  customer_original_input_tokens,
  saved_input_tokens,
  saved_input_token_percent,
  upstream_input_tokens,
  cache_read_input_tokens
FROM usage_summary;
'
```

Query by model:

```sh
sqlite3 data/usage.sqlite '
SELECT
  model,
  COUNT(*) AS requests,
  SUM(customer_original_input_tokens) AS customer_original_input_tokens,
  SUM(saved_input_tokens) AS saved_input_tokens,
  ROUND(
    100.0 * SUM(saved_input_tokens) / NULLIF(SUM(customer_original_input_tokens), 0),
    4
  ) AS saved_input_token_percent,
  checkpoint_compressor,
  checkpoint_compressor_model,
  SUM(upstream_input_tokens) AS upstream_input_tokens,
  SUM(cache_read_input_tokens) AS cache_read_input_tokens
FROM usage_records
GROUP BY model, checkpoint_compressor, checkpoint_compressor_model
ORDER BY saved_input_tokens DESC;
'
```

Important usage columns:

- `created_at`: request timestamp
- `model`: target upstream model
- `customer_original_input_tokens`: estimated input tokens before proxy compression
- `forward_estimated_input_tokens`: estimated input tokens forwarded after proxy compression
- `saved_input_tokens`: estimated input tokens saved by checkpoint compression
- `saved_input_token_percent`: `saved_input_tokens / customer_original_input_tokens * 100`
- `checkpoint_compressor`: compressor mode, for example `openai-chat`
- `checkpoint_compressor_model`: compressor model, for example `glm-5.1`
- `upstream_input_tokens` / `upstream_output_tokens`: actual upstream usage from Anthropic-compatible response
- `cache_read_input_tokens`: upstream native prompt-cache hits

`saved_input_tokens` only counts checkpoint compression savings. Anthropic native prompt-cache benefits remain separate in `cache_read_input_tokens`.
