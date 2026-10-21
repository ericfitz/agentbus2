package server

import (
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// Converters between the domain types and the generated wire types, mirroring
// internal/bus/remote/convert.go.

func derefOr[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func optMap(m *map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	return *m
}

func optSlice(s *[]string) []string {
	if s == nil {
		return nil
	}
	return *s
}

func toGenMessage(m bus.Message) gen.Message {
	out := gen.Message{
		Seq: m.Seq, Channel: m.Channel, Sender: m.Sender, Display: m.Display,
		CreatedAt: m.CreatedAt, Type: m.Type, Subject: m.Subject, Content: m.Content,
		ReplyTo: m.ReplyTo, MemoryId: m.MemoryID, Revision: m.Revision,
	}
	if m.Session != "" {
		s := m.Session
		out.Session = &s
	}
	if m.Metadata != nil {
		out.Metadata = &m.Metadata
	}
	if m.Tags != nil {
		out.Tags = &m.Tags
	}
	if m.MatchedTags != nil {
		out.MatchedTags = &m.MatchedTags
	}
	return out
}
