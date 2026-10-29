package main

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const baseNChars = "0123456789abcdefghijklmnopqrstuvwxyz"

func baseN(num, base int) string {
	if base < 2 || base > 36 {
		return ""
	}
	if num == 0 {
		return "0"
	}
	var res []byte
	for num > 0 {
		rem := num % base
		num = num / base
		res = append(res, baseNChars[rem])
	}
	for i, j := 0, len(res)-1; i < j; i, j = i+1, j-1 {
		res[i], res[j] = res[j], res[i]
	}
	return string(res)
}

var rePackedJS = regexp.MustCompile(`(?s)eval\(function\(p,a,c,k,e,d\)\{.*?\}\('([\s\S]*?)',(\d+),(\d+),'([\s\S]*?)'\.split\('\|'\)`)

func unescapeJS(s string) string {
	// Replace standard escape sequences
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case '\'':
				b.WriteByte('\'')
				i++
			case '"':
				b.WriteByte('"')
				i++
			case '\\':
				b.WriteByte('\\')
				i++
			case 'n':
				b.WriteByte('\n')
				i++
			case 'r':
				b.WriteByte('\r')
				i++
			case 't':
				b.WriteByte('\t')
				i++
			case '/':
				b.WriteByte('/')
				i++
			default:
				b.WriteByte(s[i+1])
				i++
			}
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func unpackPackedJS(html string) string {
	m := rePackedJS.FindStringSubmatch(html)
	if len(m) < 5 {
		return ""
	}

	payload := unescapeJS(m[1])
	base, err1 := strconv.Atoi(m[2])
	count, err2 := strconv.Atoi(m[3])
	if err1 != nil || err2 != nil || base <= 0 {
		return ""
	}
	words := strings.Split(m[4], "|")

	// Token replacement using word-boundary regex
	for index := count - 1; index >= 0; index-- {
		if index < len(words) && words[index] != "" {
			tok := baseN(index, base)
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(tok) + `\b`)
			payload = re.ReplaceAllString(payload, words[index])
		}
	}
	return payload
}

func main() {
	htmlBytes, err := os.ReadFile("hanerix_test/test_page.html")
	if err != nil {
		fmt.Printf("Error reading html: %v\n", err)
		return
	}
	unpacked := unpackPackedJS(string(htmlBytes))
	fmt.Printf("Unpacked length: %d\n", len(unpacked))
	expectedBytes, _ := os.ReadFile("hanerix_test/unpacked.js")
	expected := string(expectedBytes)
	if unpacked == expected {
		fmt.Println("EXACT MATCH WITH PYTHON UNPACKER!")
	} else {
		fmt.Println("Differs from python output. Checking prefix:")
		if len(unpacked) > 200 {
			fmt.Println("Go prefix:", unpacked[:200])
		}
		if len(expected) > 200 {
			fmt.Println("Python prefix:", expected[:200])
		}
	}
}
