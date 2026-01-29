package server

import (
	"fmt"
)

// Helper functions to safely extract values from interface{} slices or maps without panicking

func getFloat64(val any) (float64, error) {
	if v, ok := val.(float64); ok {
		return v, nil
	}
	if v, ok := val.(int); ok {
		return float64(v), nil
	}
	return 0, fmt.Errorf("expected float64, got %T", val)
}

func getString(val any) (string, error) {
	if v, ok := val.(string); ok {
		return v, nil
	}
	return "", fmt.Errorf("expected string, got %T", val)
}

func getMap(val any) (map[string]any, error) {
	if v, ok := val.(map[string]any); ok {
		return v, nil
	}
	return nil, fmt.Errorf("expected map[string]any, got %T", val)
}

func getBool(val any) (bool, error) {
	if v, ok := val.(bool); ok {
		return v, nil
	}
	return false, fmt.Errorf("expected bool, got %T", val)
}

// getArgAsFloat64 safely gets the argument at index i as a float64 (or int converted to float64)
func getArgAsFloat64(args []any, index int) (float64, error) {
	if index >= len(args) {
		return 0, fmt.Errorf("argument index %d out of range", index)
	}
	return getFloat64(args[index])
}

// getArgAsUint safely gets the argument at index i and converts it to uint
func getArgAsUint(args []any, index int) (uint, error) {
	f, err := getArgAsFloat64(args, index)
	if err != nil {
		return 0, err
	}
	return uint(f), nil
}

// getArgAsString safely gets the argument at index i as a string
func getArgAsString(args []any, index int) (string, error) {
	if index >= len(args) {
		return "", fmt.Errorf("argument index %d out of range", index)
	}
	return getString(args[index])
}

// getArgAsMap safely gets the argument at index i as a map
func getArgAsMap(args []any, index int) (map[string]any, error) {
	if index >= len(args) {
		return nil, fmt.Errorf("argument index %d out of range", index)
	}
	return getMap(args[index])
}

// getCallback safely gets a callback function from the arguments (usually the last one)
func getCallback(args []any) func([]any, error) {
	if len(args) > 0 {
		if cb, ok := args[len(args)-1].(func([]any, error)); ok {
			return cb
		}
	}
	return nil
}

// safeMapGetFloat64 safely gets a value from a map as float64
func safeMapGetFloat64(m map[string]any, key string) (float64, bool) {
	val, ok := m[key]
	if !ok {
		return 0, false
	}
	f, err := getFloat64(val)
	return f, err == nil
}

// safeMapGetString safely gets a value from a map as string
func safeMapGetString(m map[string]any, key string) string {
	val, ok := m[key]
	if !ok {
		return ""
	}
	s, err := getString(val)
	if err != nil {
		return ""
	}
	return s
}
