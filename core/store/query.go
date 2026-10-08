package store

import "fmt"

// Int converts a sqlite value to int64 (NULL and non-integers give 0).
func Int(v any) int64 {
	n, _ := v.(int64)
	return n
}

// Str converts a sqlite value to string (NULL gives "").
func Str(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}
