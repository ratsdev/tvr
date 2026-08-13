package utils

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// NaturalCompare orders Latin/English before CJK, with numeric-aware ordering within each group.
// Equal numeric values prefer fewer leading zeros (CCTV-1 before CCTV-01).
func NaturalCompare(a, b string) int {
	ac := startsWithCJK(a)
	bc := startsWithCJK(b)
	if ac != bc {
		if ac {
			return 1
		}
		return -1
	}
	return compareNatural(a, b)
}

// NaturalLess reports whether a should sort before b.
func NaturalLess(a, b string) bool {
	return NaturalCompare(a, b) < 0
}

func startsWithCJK(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return false
	}
	return unicode.In(r, unicode.Han) ||
		(r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0x20000 && r <= 0x2CEAF)
}

func compareNatural(a, b string) int {
	ra := []rune(strings.ToLower(a))
	rb := []rune(strings.ToLower(b))
	i, j := 0, 0
	for i < len(ra) && j < len(rb) {
		da, db := unicode.IsDigit(ra[i]), unicode.IsDigit(rb[j])
		if da && db {
			ia, ja := i, j
			for ia < len(ra) && ra[ia] == '0' {
				ia++
			}
			for ja < len(rb) && rb[ja] == '0' {
				ja++
			}
			ea, eb := ia, ja
			for ea < len(ra) && unicode.IsDigit(ra[ea]) {
				ea++
			}
			for eb < len(rb) && unicode.IsDigit(rb[eb]) {
				eb++
			}
			lena, lenb := ea-ia, eb-ja
			if lena != lenb {
				if lena < lenb {
					return -1
				}
				return 1
			}
			for k := 0; k < lena; k++ {
				if ra[ia+k] != rb[ja+k] {
					if ra[ia+k] < rb[ja+k] {
						return -1
					}
					return 1
				}
			}
			// Equal value: shorter digit run (fewer leading zeros) first.
			if (ea - i) != (eb - j) {
				if (ea - i) < (eb - j) {
					return -1
				}
				return 1
			}
			i, j = ea, eb
			continue
		}
		if ra[i] != rb[j] {
			if ra[i] < rb[j] {
				return -1
			}
			return 1
		}
		i++
		j++
	}
	switch {
	case i == len(ra) && j == len(rb):
		return 0
	case i == len(ra):
		return -1
	default:
		return 1
	}
}
