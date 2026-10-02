package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// node is an order-preserving JSON value. encoding/json keeps the LAST of
// two duplicate keys and matches keys case-insensitively, so a target that
// production would silently accept can still be ambiguous training data.
// This tree keeps every key, in order, so those cases can be reported.
type nodeKind int

const (
	nodeNull nodeKind = iota
	nodeBool
	nodeNumber
	nodeString
	nodeArray
	nodeObject
)

type member struct {
	Key   string
	Value *node
}

type node struct {
	Kind    nodeKind
	Bool    bool
	Number  json.Number
	String  string
	Items   []*node
	Members []member
}

// parseOrdered parses exactly one JSON value. Trailing non-space data is an
// error.
func parseOrdered(data []byte) (*node, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := parseValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, errors.New("trailing data after JSON value")
		}
		return nil, err
	}
	return value, nil
}

func parseValue(decoder *json.Decoder) (*node, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	return parseFromToken(decoder, token)
}

func parseFromToken(decoder *json.Decoder, token json.Token) (*node, error) {
	switch value := token.(type) {
	case nil:
		return &node{Kind: nodeNull}, nil
	case bool:
		return &node{Kind: nodeBool, Bool: value}, nil
	case json.Number:
		return &node{Kind: nodeNumber, Number: value}, nil
	case string:
		return &node{Kind: nodeString, String: value}, nil
	case json.Delim:
		switch value {
		case '{':
			object := &node{Kind: nodeObject}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("object key is %T, not a string", keyToken)
				}
				child, err := parseValue(decoder)
				if err != nil {
					return nil, err
				}
				object.Members = append(object.Members, member{Key: key, Value: child})
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return object, nil
		case '[':
			array := &node{Kind: nodeArray}
			for decoder.More() {
				child, err := parseValue(decoder)
				if err != nil {
					return nil, err
				}
				array.Items = append(array.Items, child)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return array, nil
		}
	}
	return nil, fmt.Errorf("unexpected JSON token %v", token)
}

// duplicatePaths lists every object key that appears more than once in its
// object, as a JSONPath-like string ($.a.b[2].c).
func (n *node) duplicatePaths() []string {
	var out []string
	n.walk("$", func(path string, current *node) {
		if current.Kind != nodeObject {
			return
		}
		seen := make(map[string]bool, len(current.Members))
		for _, m := range current.Members {
			if seen[m.Key] {
				out = append(out, childPath(path, m.Key))
			}
			seen[m.Key] = true
		}
	})
	return out
}

func (n *node) walk(path string, visit func(string, *node)) {
	visit(path, n)
	switch n.Kind {
	case nodeObject:
		for _, m := range n.Members {
			m.Value.walk(childPath(path, m.Key), visit)
		}
	case nodeArray:
		for i, item := range n.Items {
			item.walk(path+"["+strconv.Itoa(i)+"]", visit)
		}
	}
}

func childPath(parent, key string) string {
	return parent + "." + key
}

// lookup returns the LAST value for key, mirroring encoding/json's
// last-duplicate-wins behaviour.
func (n *node) lookup(key string) (*node, bool) {
	if n == nil || n.Kind != nodeObject {
		return nil, false
	}
	var found *node
	for _, m := range n.Members {
		if m.Key == key {
			found = m.Value
		}
	}
	return found, found != nil
}
