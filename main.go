package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var entryKeys = map[string]string{
	"AUTHOR":           "author",
	"TITLE":            "title",
	"BASENAME":         "permalink",
	"STATUS":           "status",
	"ALLOW COMMENTS":   "",
	"ALLOW PINGS":      "",
	"PRIMARY CATEGORY": "primary_category",
	"CATEGORY":         "category",
	"TAGS":             "tags",
	// handled in code: "CONVERT BREAKS", "DATE"
}

type comment struct {
	Author  string
	Email   string
	URL     string
	Date    time.Time
	Content string
}

type entry struct {
	date          time.Time
	header        map[string]string
	content       bytes.Buffer
	comments      []*comment
	convertBreaks bool
}

func NewEntry() *entry {
	return &entry{
		header: make(map[string]string),
	}
}

func (e *entry) WriteToFile(dir string) error {
	name, ok := e.header["permalink"]
	if !ok {
		return errors.New("no permalink in entry")
	}
	name, err := strconv.Unquote(name)
	if err != nil {
		return err
	}
	name = strings.Replace(name, "_", "-", -1)
	filename := e.date.Format("2006-01-02-") + name + ".html"
	log.Printf("Writing %s", filename)
	delete(e.header, "permalink")

	body := e.content.Bytes()
	if e.header["markup"] == "textile" {
		// Convert textile to HTML with redcloth.
		cmd := exec.Command("redcloth")
		cmd.Stdin = bytes.NewReader(body)
		var out bytes.Buffer
		cmd.Stdout = &out
		err := cmd.Run()
		if err != nil {
			log.Fatal(err)
		}
		body = out.Bytes()
		delete(e.header, "markup")
		log.Printf("*** Converted textile")
	}

	buf := new(bytes.Buffer)
	header := make([]string, 0)
	for k, v := range e.header {
		header = append(header, k+": "+v+"\n")
	}
	sort.Strings(header)
	// Write header
	buf.WriteString("---\n")
	for _, v := range header {
		if _, err := buf.WriteString(v); err != nil {
			return err
		}
	}
	buf.WriteString("---\n")
	// Write body
	buf.Write(body)
	// Append comments.
	if len(e.comments) > 0 {
		buf.WriteString("\n\n<div class=\"comments\">\n")
		for _, c := range e.comments {
			buf.WriteString("<div class=\"comment\">\n")
			buf.WriteString("<div class=\"comment-header\">\n")
			buf.WriteString("<span class=\"comment-author\">")
			if c.URL != "" {
				fmt.Fprintf(buf, "<a rel=\"nofollow\" href=\"%s\">%s</a>", c.URL, c.Author)
			} else {
				buf.WriteString(c.Author)
			}
			fmt.Fprintf(buf, "</span> <span class=\"comment-date\">%s</span>\n", c.Date.Format("2006-01-02 15:06"))
			buf.WriteString("</div>\n")
			buf.WriteString("<div class=\"comment-body\">\n")
			buf.WriteString(c.Content)
			buf.WriteString("</div>\n")
			buf.WriteString("</div>\n")
		}
		buf.WriteString("</div>\n")
	}
	// Output to file
	return ioutil.WriteFile(filepath.Join(dir, filename), buf.Bytes(), 0644)
}

type scanner struct {
	bufio.Scanner
	eof bool
}

const sectionMarker = "-----"
const entryMarker = "--------"

func (s *scanner) entryHeaderItem(e *entry) bool {
	if !s.Scan() {
		if s.Err() == nil {
			s.eof = true
			return false
		}
		log.Fatal(s.Err())
	}
	text := s.Text()
	if text == sectionMarker {
		// End of section.
		return false
	}
	if text == "" {
		return true
	}
	kv := strings.SplitN(text, ":", 2)
	if len(kv) != 2 {
		log.Fatalf("unexpected `%s`", text)
	}
	val := strings.TrimSpace(kv[1])
	key, ok := entryKeys[kv[0]]
	if !ok {
		switch kv[0] {
		case "DATE":
			date, err := time.Parse("01/02/2006 3:04:05 PM", val)
			if err != nil {
				log.Fatal(err)
			}
			e.date = date
			e.header["date"] = date.Format("2006-01-02 15:04:05 -07:00")
			return true
		case "CONVERT BREAKS":
			switch val {
			case "markdown", "markdown_with_smartypants":
				e.header["markup"] = "markdown"
				return true
			case "1", "__default__":
				e.convertBreaks = true
				return true
			case "0":
				e.convertBreaks = false
				return true
			case "textile", "textile_2":
				e.header["markup"] = "textile"
			default:
				log.Fatalf("unsupported markup %s", val)
			}
		default:
			log.Fatalf("unknown header key `%s`", kv[0])
		}
	}
	if key == "" {
		return true
	}
	e.header[key] = strconv.Quote(val)
	return true
}

func (s *scanner) entryHeader(e *entry) {
	for s.entryHeaderItem(e) {
	}
}

func (s *scanner) nextSection() (name string, ok bool) {
	for {
		if !s.Scan() {
			if s.Err() == nil {
				log.Fatalf("unexpected end of file")
			}
			log.Fatal(s.Err())
		}
		name = s.Text()
		if name == entryMarker {
			return "", false
		}
		if name != "" {
			return name, true
		}
	}
}

func (s *scanner) entryBody(e *entry) {
	for s.Scan() {
		text := s.Text()
		if text == sectionMarker {
			return
		}
		if e.convertBreaks && !strings.HasPrefix(text, "<p ") && !strings.HasPrefix(text, "<p>") {
			if text == "" {
				continue
			}
			e.content.WriteString("<p>" + text + "</p>\n")
		} else {
			e.content.WriteString(text + "\n")
		}
	}
	log.Fatalf("unterminated body")
}

func (s *scanner) scanCommentItem(key string) (value string) {
	if !s.Scan() {
		log.Fatalf("expecting %s", key)
	}
	kv := strings.SplitN(s.Text(), ":", 2)
	if len(kv) != 2 {
		log.Fatalf("wrong format %s", key)
	}
	if kv[0] != key {
		log.Fatalf("expected %s, got %s", key, kv[0])
	}
	return strings.TrimSpace(kv[1])
}

func (s *scanner) scanComment() *comment {
	// Header.
	c := new(comment)
	c.Author = s.scanCommentItem("AUTHOR")
	c.Email = s.scanCommentItem("EMAIL")
	s.scanCommentItem("IP")
	c.URL = s.scanCommentItem("URL")
	date, err := time.Parse("01/02/2006 3:04:05 PM", s.scanCommentItem("DATE"))
	if err != nil {
		log.Fatalf("parsing comment date: %s", err)
	}
	c.Date = date

	var buf bytes.Buffer
	for s.Scan() {
		text := s.Text()
		if text == sectionMarker {
			c.Content = buf.String()
			return c
		}
		if text != "" {
			buf.WriteString("<p>" + text + "</p>\n")
		}
	}
	log.Fatalf("unterminated comment body")
	return nil
}

func (s *scanner) skipSection() {
	for s.Scan() {
		if s.Text() == sectionMarker {
			return
		}
	}
	if s.Err() != nil {
		log.Fatal(s.Err())
	}
	log.Fatalf("unexpected end of section")

}

func (s *scanner) entry(dir string) bool {
	e := NewEntry()
	s.entryHeader(e)
	if s.eof {
		return false
	}
	for {
		name, ok := s.nextSection()
		if !ok {
			break
		}
		switch name {
		case "BODY:", "EXTENDED BODY:":
			s.entryBody(e)
		case "EXCERPT:", "KEYWORDS:", "PING:":
			s.skipSection()
		case "COMMENT:":
			e.comments = append(e.comments, s.scanComment())
		default:
			log.Fatalf("unknown section %s", name)
		}
	}
	if err := e.WriteToFile(dir); err != nil {
		log.Fatal(err)
	}
	return true
}

func importReader(r io.Reader, dir string) {
	s := scanner{*bufio.NewScanner(r), false}
	for s.entry(dir) {
	}
}

func main() {
	flag.Parse()
	if flag.NArg() < 1 {
		log.Fatalf("usage: mt2kkr outdir < input.txt")
	}
	dir := flag.Arg(0)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Fatal(err)
	}
	importReader(os.Stdin, dir)
}
