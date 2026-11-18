package core

import (
	"strconv"
	"strings"
)

func trim(s string) string { return strings.TrimSpace(s) }
func itoa(i int) string    { return strconv.Itoa(i) }
