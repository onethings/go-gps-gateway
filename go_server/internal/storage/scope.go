package postgres

import (
	"fmt"
	"strings"
)

// deviceIDsClause scopes a statement to a set of device ids. Nil means "no
// restriction" and is handled by the caller; an empty slice deliberately
// matches nothing so a token without device grants cannot read every device by
// accident. The ids become ordinary placeholders, which keeps the statement
// portable and avoids driver-specific slice encoding.
func deviceIDsClause(args *[]any, column string, ids []int64) string {
	if len(ids) == 0 {
		return "false"
	}

	placeholders := make([]string, 0, len(ids))
	for _, id := range ids {
		*args = append(*args, id)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(*args)))
	}
	return fmt.Sprintf("%s IN (%s)", column, strings.Join(placeholders, ", "))
}

// textIDsClause is the text counterpart of deviceIDsClause: an exact match
// against a list of strings, used by the uniqueId lookup.
func textIDsClause(args *[]any, column string, values []string) string {
	if len(values) == 0 {
		return "false"
	}

	placeholders := make([]string, 0, len(values))
	for _, value := range values {
		*args = append(*args, value)
		placeholders = append(placeholders, fmt.Sprintf("$%d", len(*args)))
	}
	return fmt.Sprintf("%s IN (%s)", column, strings.Join(placeholders, ", "))
}
