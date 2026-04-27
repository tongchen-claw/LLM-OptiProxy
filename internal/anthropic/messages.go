package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

type Request struct {
	Raw      map[string]json.RawMessage
	Messages []Message
	Stream   bool
	Model    string
	Body     []byte
}

type Message struct {
	Raw     map[string]json.RawMessage
	Role    string
	Content json.RawMessage
}

func ParseRequest(body []byte) (*Request, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	var messages []Message
	if messagesRaw, ok := raw["messages"]; ok {
		if err := json.Unmarshal(messagesRaw, &messages); err != nil {
			return nil, fmt.Errorf("parse messages: %w", err)
		}
	}

	var stream bool
	if streamRaw, ok := raw["stream"]; ok {
		_ = json.Unmarshal(streamRaw, &stream)
	}

	var model string
	if modelRaw, ok := raw["model"]; ok {
		_ = json.Unmarshal(modelRaw, &model)
	}

	return &Request{
		Raw:      raw,
		Messages: messages,
		Stream:   stream,
		Model:    model,
		Body:     bytes.Clone(body),
	}, nil
}

func (r *Request) MarshalWithMessages(messages []Message) ([]byte, error) {
	if r == nil || r.Raw == nil {
		return nil, errors.New("nil request")
	}
	cloned := make(map[string]json.RawMessage, len(r.Raw))
	for key, value := range r.Raw {
		cloned[key] = value
	}

	messagesRaw, err := json.Marshal(messages)
	if err != nil {
		return nil, err
	}
	cloned["messages"] = messagesRaw
	return json.Marshal(cloned)
}

func (r *Request) RawField(name string) (json.RawMessage, bool) {
	if r == nil || r.Raw == nil {
		return nil, false
	}
	value, ok := r.Raw[name]
	return value, ok
}

func (m *Message) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Raw = raw
	if roleRaw, ok := raw["role"]; ok {
		_ = json.Unmarshal(roleRaw, &m.Role)
	}
	if contentRaw, ok := raw["content"]; ok {
		m.Content = bytes.Clone(contentRaw)
	}
	return nil
}

func (m Message) MarshalJSON() ([]byte, error) {
	if m.Raw != nil {
		raw := make(map[string]json.RawMessage, len(m.Raw))
		for key, value := range m.Raw {
			raw[key] = value
		}
		if m.Role != "" {
			roleRaw, err := json.Marshal(m.Role)
			if err != nil {
				return nil, err
			}
			raw["role"] = roleRaw
		}
		if m.Content != nil {
			raw["content"] = m.Content
		}
		return json.Marshal(raw)
	}

	roleRaw, err := json.Marshal(m.Role)
	if err != nil {
		return nil, err
	}
	raw := map[string]json.RawMessage{"role": roleRaw}
	if m.Content != nil {
		raw["content"] = m.Content
	}
	return json.Marshal(raw)
}

func NewTextMessage(role, text string) Message {
	roleRaw, _ := json.Marshal(role)
	contentRaw, _ := json.Marshal(text)
	return Message{
		Raw: map[string]json.RawMessage{
			"role":    roleRaw,
			"content": contentRaw,
		},
		Role:    role,
		Content: contentRaw,
	}
}

func (m Message) TextSnippet(limit int) string {
	if limit <= 0 || len(m.Content) == 0 {
		return ""
	}

	var text string
	if err := json.Unmarshal(m.Content, &text); err == nil {
		return truncate(text, limit)
	}

	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(m.Content, &blocks); err == nil {
		var buf bytes.Buffer
		for _, block := range blocks {
			var blockType string
			_ = json.Unmarshal(block["type"], &blockType)
			switch blockType {
			case "text":
				var blockText string
				_ = json.Unmarshal(block["text"], &blockText)
				if blockText != "" {
					if buf.Len() > 0 {
						buf.WriteString("\n")
					}
					buf.WriteString(blockText)
				}
			case "tool_use", "tool_result":
				if buf.Len() > 0 {
					buf.WriteString("\n")
				}
				buf.WriteString("[")
				buf.WriteString(blockType)
				buf.WriteString("]")
			}
			if buf.Len() >= limit {
				break
			}
		}
		if buf.Len() > 0 {
			return truncate(buf.String(), limit)
		}
	}

	return truncate(string(m.Content), limit)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}
