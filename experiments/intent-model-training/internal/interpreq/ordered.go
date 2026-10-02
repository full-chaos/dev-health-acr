package interpreq

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// Ported verbatim from gohelper/ordered.go (gohelper moves onto this package
// after the review session). encoding/json keeps the LAST duplicate key, so
// duplicates are detected on an order-preserving tree.

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

func (n *node) duplicatePaths() []string {
	var out []string
	n.walk("$", func(path string, current *node) {
		if current.Kind != nodeObject {
			return
		}
		seen := make(map[string]bool, len(current.Members))
		for _, m := range current.Members {
			if seen[m.Key] {
				out = append(out, path+"."+m.Key)
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
			m.Value.walk(path+"."+m.Key, visit)
		}
	case nodeArray:
		for i, item := range n.Items {
			item.walk(path+"["+strconv.Itoa(i)+"]", visit)
		}
	}
}

// DuplicateKeys reports duplicate object keys anywhere in data, or a parse error.
func DuplicateKeys(data []byte) ([]string, error) {
	tree, err := parseOrdered(bytes.TrimSpace(data))
	if err != nil {
		return nil, err
	}
	return tree.duplicatePaths(), nil
}
