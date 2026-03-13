package sse

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
)

type Event struct {
	Event string
	Data  string
}

func ParseStream(r io.Reader, fn func(Event) error) error {
	reader := bufio.NewReader(r)
	var (
		eventName string
		dataLines []string
	)

	flush := func() error {
		if len(dataLines) == 0 && eventName == "" {
			return nil
		}
		data := strings.Join(dataLines, "\n")
		event := Event{Event: eventName, Data: data}
		eventName = ""
		dataLines = dataLines[:0]
		return fn(event)
	}

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return err
		}
		line = bytes.TrimRight(line, "\r\n")
		if len(line) == 0 {
			if err := flush(); err != nil {
				return err
			}
			if err == io.EOF {
				return nil
			}
			continue
		}

		if bytes.HasPrefix(line, []byte(":")) {
			if err == io.EOF {
				return flush()
			}
			continue
		}

		field, value, ok := bytes.Cut(line, []byte(":"))
		if !ok {
			field = line
			value = nil
		} else if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}

		switch string(field) {
		case "event":
			eventName = string(value)
		case "data":
			dataLines = append(dataLines, string(value))
		}

		if err == io.EOF {
			return flush()
		}
	}
}

func WriteEvent(w io.Writer, event string, payload string) error {
	if event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
			return err
		}
	}
	for _, line := range strings.Split(payload, "\n") {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "\n")
	return err
}
