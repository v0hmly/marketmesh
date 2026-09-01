package topology

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const targetSnapshotInputLimit = 1 << 20

// DecodeTargetSnapshot strictly decodes one bounded snapshot document.
func DecodeTargetSnapshot(reader io.Reader) (TargetSnapshot, error) {
	snapshot := TargetSnapshot{}
	if err := decodeTargetDocument(reader, &snapshot); err != nil {
		return TargetSnapshot{}, err
	}
	return snapshot, nil
}

// DecodeTargetRebindInput strictly decodes one bounded transition document.
func DecodeTargetRebindInput(reader io.Reader) (TargetRebindInput, error) {
	input := TargetRebindInput{}
	if err := decodeTargetDocument(reader, &input); err != nil {
		return TargetRebindInput{}, err
	}
	return input, nil
}

func decodeTargetDocument(reader io.Reader, destination any) error {
	data, err := io.ReadAll(io.LimitReader(reader, targetSnapshotInputLimit+1))
	if err != nil {
		return fmt.Errorf("reading target document: %w", err)
	}
	if len(data) > targetSnapshotInputLimit {
		return errors.New("topology: target document exceeds input limit")
	}
	if err := rejectDuplicateJSONFields(data); err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decoding target document: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	return nil
}

func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := checkJSONValue(decoder); err != nil {
		return fmt.Errorf("decoding target snapshot structure: %w", err)
	}
	return requireJSONEOF(decoder)
}

func checkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		fields := map[string]struct{}{}
		for decoder.More() {
			fieldToken, fieldErr := decoder.Token()
			if fieldErr != nil {
				return fieldErr
			}
			field, ok := fieldToken.(string)
			if !ok {
				return errors.New("topology: json object field is not a string")
			}
			if _, exists := fields[field]; exists {
				return fmt.Errorf("topology: duplicate json field %q", field)
			}
			fields[field] = struct{}{}
			if err := checkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("topology: invalid json object closing delimiter")
		}
	case '[':
		for decoder.More() {
			if err := checkJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("topology: invalid json array closing delimiter")
		}
	default:
		return errors.New("topology: invalid json opening delimiter")
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("decoding target snapshot trailer: %w", err)
	}
	return errors.New("topology: target snapshot contains trailing json")
}
