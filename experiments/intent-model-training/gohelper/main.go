// Command interp-helper exposes the production ACR interpretation exchange
// (system prompt, user payload, output schema, parser, validators and the
// RuntimeQuestionInterpreter consumer) to the offline training pipeline.
//
// Read-only by construction: it builds no model client, opens no network
// connection, starts no service and performs no authorization. The contract
// is experiments/intent-model-training/INTERFACES.md section 6.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const usage = `usage: interp-helper <version|system-prompt|system-message|schema|render|validate|validate-batch>
JSON on stdin (render, validate, validate-batch), JSON on stdout.
Exit 0 when the helper ran (an invalid target is data), 2 on usage or internal error.`

// maxBatchLineBytes bounds one validate-batch input line.
const maxBatchLineBytes = 16 << 20

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "version":
		err = writeJSON(stdout, versionInfo())
	case "system-prompt":
		err = writeJSON(stdout, systemPromptInfo())
	case "system-message":
		var out systemMessageOutput
		out, err = systemMessageInfo()
		if err == nil {
			err = writeJSON(stdout, out)
		}
	case "schema":
		var out schemaOutput
		out, err = schemaInfo()
		if err == nil {
			err = writeJSON(stdout, out)
		}
	case "render":
		err = runRender(stdin, stdout)
	case "validate":
		err = runValidate(stdin, stdout)
	case "validate-batch":
		err = runValidateBatch(stdin, stdout)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "interp-helper: unknown subcommand %q\n%s\n", args[0], usage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "interp-helper %s: %v\n", args[0], err)
		return 2
	}
	return 0
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

type renderInput struct {
	Request json.RawMessage `json:"request"`
}

func runRender(stdin io.Reader, stdout io.Writer) error {
	var in renderInput
	if err := decodeEnvelope(stdin, &in); err != nil {
		return err
	}
	out, err := render(in.Request)
	if err != nil {
		return err
	}
	return writeJSON(stdout, out)
}

type validateInput struct {
	ID        json.RawMessage `json:"id,omitempty"`
	Raw       *string         `json:"raw"`
	Request   json.RawMessage `json:"request"`
	Transport string          `json:"transport,omitempty"`
}

func runValidate(stdin io.Reader, stdout io.Writer) error {
	var in validateInput
	if err := decodeEnvelope(stdin, &in); err != nil {
		return err
	}
	if in.Raw == nil {
		return errors.New(`input requires "raw" (string)`)
	}
	env, err := validate(*in.Raw, in.Request, in.Transport)
	if err != nil {
		return err
	}
	return writeJSON(stdout, env)
}

// batchResult is one validate-batch output line: the caller's id plus the
// validate envelope, flattened into one object.
type batchResult struct {
	ID json.RawMessage `json:"id"`
	*Envelope
	HelperError string `json:"helper_error,omitempty"`
}

func runValidateBatch(stdin io.Reader, stdout io.Writer) error {
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 1<<20), maxBatchLineBytes)
	writer := bufio.NewWriter(stdout)
	defer writer.Flush()
	line := 0
	for scanner.Scan() {
		line++
		text := bytes.TrimSpace(scanner.Bytes())
		if len(text) == 0 {
			continue
		}
		var in validateInput
		if err := decodeStrictBytes(text, &in); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		if len(in.ID) == 0 {
			return fmt.Errorf(`line %d: input requires "id"`, line)
		}
		if in.Raw == nil {
			return fmt.Errorf(`line %d: input requires "raw" (string)`, line)
		}
		result := batchResult{ID: in.ID}
		env, err := validate(*in.Raw, in.Request, in.Transport)
		if err != nil {
			// A malformed request is a per-line helper error, not a batch
			// abort: the caller still gets one line per input id.
			result.HelperError = err.Error()
		} else {
			result.Envelope = &env
		}
		if err := writeJSON(writer, result); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func decodeEnvelope(stdin io.Reader, target any) error {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	return decodeStrictBytes(bytes.TrimSpace(data), target)
}

func decodeStrictBytes(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode input: %w", err)
	}
	if decoder.More() {
		return errors.New("decode input: trailing data after JSON value")
	}
	return nil
}
