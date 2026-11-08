package dnsx_test

// Chapter 10 — DNS messages by hand: the 12-byte header, names and compression pointers, answers.
// Run: go test ./internal/dnsx -run Ch10 -race -v
//
// Nothing in this file builds an input with the package under test: if encoder and decoder agreed on the same
// mistake, the tests would pass anyway. Every packet below is assembled from raw bytes and label helpers, and the
// expected golden query is written out byte for byte.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"netlab/internal/dnsx"
	"netlab/internal/testutil"
)

// ------------------------------------------------------------------ hand-built wire bytes

// testLabels encodes a name as length-prefixed labels terminated by 0x00, WITHOUT using the package under test.
func testLabels(parts ...string) []byte {
	b := make([]byte, 0, 16)
	for _, p := range parts {
		b = append(b, byte(len(p)))
		b = append(b, p...)
	}
	return append(b, 0x00)
}

func be16(b []byte, v uint16) []byte {
	var tmp [2]byte
	binary.BigEndian.PutUint16(tmp[:], v)
	return append(b, tmp[:]...)
}

func be32(b []byte, v uint32) []byte {
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], v)
	return append(b, tmp[:]...)
}

func testHeader(id, flags uint16, qd, an, ns, ar int) []byte {
	h := make([]byte, 12)
	binary.BigEndian.PutUint16(h[0:], id)
	binary.BigEndian.PutUint16(h[2:], flags)
	binary.BigEndian.PutUint16(h[4:], uint16(qd))
	binary.BigEndian.PutUint16(h[6:], uint16(an))
	binary.BigEndian.PutUint16(h[8:], uint16(ns))
	binary.BigEndian.PutUint16(h[10:], uint16(ar))
	return h
}

// testQuestion is the example.com/A/IN question every crafted response carries.
func testQuestion() []byte {
	q := testLabels("example", "com")
	q = be16(q, dnsx.TypeA)
	q = be16(q, dnsx.ClassIN)
	return q
}

// ptrToQuestion is the two-byte compression pointer saying "the name here is the one at offset 12", i.e. the
// question's example.com. This is the trickiest two bytes in the whole chapter — mind them.
var ptrToQuestion = []byte{0xC0, 0x0C}

// testRecord builds one resource record from raw pieces. name is the owner name as wire bytes — pass
// ptrToQuestion to point at the question name, or a hand-encoded name.
func testRecord(name []byte, typ, class uint16, ttl uint32, rdata []byte) []byte {
	b := append([]byte(nil), name...)
	b = be16(b, typ)
	b = be16(b, class)
	b = be32(b, ttl)
	b = be16(b, uint16(len(rdata)))
	return append(b, rdata...)
}

func testA(ttl uint32, ip [4]byte) []byte {
	return testRecord(ptrToQuestion, dnsx.TypeA, dnsx.ClassIN, ttl, ip[:])
}

// respFlags is a typical recursive-resolver response: QR + RD + RA, rcode 0.
const respFlags = dnsx.FlagQR | dnsx.FlagRD | dnsx.FlagRA

// craftResponse builds a response with no authority or additional sections.
func craftResponse(id, flags uint16, answers ...[]byte) []byte {
	return craftResponseSections(id, flags, answers, nil, nil)
}

// craftResponseSections is craftResponse for messages that also carry authority and/or additional records
// (e.g. an SOA after an NXDOMAIN, or an EDNS(0) OPT record).
func craftResponseSections(id, flags uint16, answers, authority, additional [][]byte) []byte {
	b := testHeader(id, flags, 1, len(answers), len(authority), len(additional))
	b = append(b, testQuestion()...)
	for _, section := range [][][]byte{answers, authority, additional} {
		for _, rr := range section {
			b = append(b, rr...)
		}
	}
	return b
}

// testResponseWithSOA builds a realistic response carrying all four sections: one A answer, an SOA in the
// authority section (nameservers shed these on NXDOMAIN/NOERROR empty-answer replies), and a root-owner OPT
// record in the additional section (EDNS(0)).
func testResponseWithSOA() []byte {
	soaRData := append(testLabels("ns1", "example", "com"), testLabels("hostmaster", "example", "com")...)
	soaRData = append(soaRData, make([]byte, 20)...) // serial, refresh, retry, expire, minimum
	soa := testRecord(ptrToQuestion, 6 /* SOA */, dnsx.ClassIN, 3600, soaRData)
	opt := testRecord([]byte{0x00} /* root */, 41 /* OPT */, 4096 /* UDP payload size */, 0, nil)
	return craftResponseSections(0xBEEF, respFlags, [][]byte{testA(300, [4]byte{9, 9, 9, 9})}, [][]byte{soa}, [][]byte{opt})
}

// exampleComQueryBytes is the hand-computed wire form of `example.com A IN` with RD set:
//
//	header:   12 34 | 01 00 | 00 01 | 00 00 | 00 00 | 00 00
//	             ID    flags    QD=1    AN=0    NS=0    AR=0
//	question: 07 example  03 com  00  |  00 01  |  00 01
//	                      QNAME           QTYPE    QCLASS
var exampleComQueryBytes = []byte{
	0x12, 0x34, // ID
	0x01, 0x00, // flags: QR=0 opcode=0 AA=0 TC=0 RD=1 RA=0 rcode=0
	0x00, 0x01, // QDCOUNT
	0x00, 0x00, // ANCOUNT
	0x00, 0x00, // NSCOUNT
	0x00, 0x00, // ARCOUNT
	0x07, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
	0x03, 'c', 'o', 'm',
	0x00,
	0x00, 0x01, // QTYPE = A
	0x00, 0x01, // QCLASS = IN
}

// queryWithID returns the golden query with its first two bytes (the ID) replaced by id.
func queryWithID(id uint16) []byte {
	b := append([]byte(nil), exampleComQueryBytes...)
	binary.BigEndian.PutUint16(b, id)
	return b
}

// scriptedTransport is the in-memory "server": it records the query it was handed and replies with whatever the
// test scripted. No sockets, no DNS, no clock.
type scriptedTransport struct {
	reply    []byte
	err      error
	gotQuery []byte
	calls    int
}

var errScriptedTransport = errors.New("scripted transport: connection timed out")

func (s *scriptedTransport) RoundTrip(query []byte) ([]byte, error) {
	s.calls++
	s.gotQuery = append([]byte(nil), query...)
	if s.err != nil {
		return nil, s.err
	}
	return append([]byte(nil), s.reply...), nil
}

// ------------------------------------------------------------------ the tests

func TestCh10_NewQuery_BuildsTheGoldenExampleDotComQuery(t *testing.T) {
	testutil.NoLeaks(t)

	m := dnsx.NewQuery(0x1234, "example.com", dnsx.TypeA)
	if m == nil {
		t.Fatal("NewQuery returned nil")
	}
	if m.ID != 0x1234 {
		t.Errorf("ID = %#04x, want 0x1234", m.ID)
	}
	if m.Flags != dnsx.FlagRD {
		t.Errorf("Flags = %#04x, want %#04x (RD only)", m.Flags, dnsx.FlagRD)
	}
	if len(m.Answers) != 0 {
		t.Errorf("Answers = %v, want none in a query", m.Answers)
	}
	if len(m.Questions) != 1 {
		t.Fatalf("Questions = %v, want exactly one", m.Questions)
	}
	q := m.Questions[0]
	if q.Name != "example.com" || q.Type != dnsx.TypeA || q.Class != dnsx.ClassIN {
		t.Errorf("question = %+v, want {example.com A IN}", q)
	}

	got, err := dnsx.EncodeMessage(m)
	if err != nil {
		t.Fatalf("EncodeMessage: %v", err)
	}
	if !bytes.Equal(got, exampleComQueryBytes) {
		t.Fatalf("the wire query is wrong:\ngot  % x\nwant % x", got, exampleComQueryBytes)
	}
	if len(got) != 29 {
		t.Errorf("encoded query is %d bytes, want 29", len(got))
	}

	// The same bytes decode back to the same fields.
	back, err := dnsx.ParseMessage(got)
	if err != nil {
		t.Fatalf("ParseMessage(our own query): %v", err)
	}
	if back.ID != 0x1234 || back.Flags != 0x0100 || len(back.Questions) != 1 || back.Questions[0] != q {
		t.Fatalf("round trip changed the message: %+v", back)
	}
}

func TestCh10_EncodeMessage_RejectsAMessageWithoutAQuestion(t *testing.T) {
	testutil.NoLeaks(t)

	for _, m := range []*dnsx.Message{nil, {ID: 1, Flags: dnsx.FlagRD}} {
		_, err := dnsx.EncodeMessage(m)
		testutil.ExpectError(t, err)
		if !errors.Is(err, dnsx.ErrFormat) {
			t.Errorf("EncodeMessage(%+v) = %v, want an error wrapping ErrFormat", m, err)
		}
	}
}

func TestCh10_EncodeMessage_RoundTripsThroughParseMessage(t *testing.T) {
	testutil.NoLeaks(t)

	want := &dnsx.Message{
		ID:    0xBEEF,
		Flags: dnsx.FlagQR | dnsx.FlagRD | dnsx.FlagRA,
		Questions: []dnsx.Question{
			{Name: "example.com", Type: dnsx.TypeA, Class: dnsx.ClassIN},
		},
		Answers: []dnsx.ResourceRecord{
			{Name: "example.com", Type: dnsx.TypeA, Class: dnsx.ClassIN, TTL: 3600, RData: []byte{93, 184, 216, 34}},
			{Name: "example.com", Type: dnsx.TypeTXT, Class: dnsx.ClassIN, TTL: 60, RData: []byte{5, 'h', 'e', 'l', 'l', 'o'}},
			{Name: ".", Type: dnsx.TypeNS, Class: dnsx.ClassIN, TTL: 172800, RData: testLabels("a", "root-servers", "net")},
		},
	}

	wire, err := dnsx.EncodeMessage(want)
	if err != nil {
		t.Fatalf("EncodeMessage: %v", err)
	}
	if got := binary.BigEndian.Uint16(wire[6:8]); got != 3 {
		t.Errorf("ANCOUNT = %d, want 3", got)
	}

	got, err := dnsx.ParseMessage(wire)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed the message:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestCh10_EncodeName_EncodesLabelsRootAndTrailingDot(t *testing.T) {
	testutil.NoLeaks(t)

	// The root name is a single zero byte.
	if got, err := dnsx.EncodeName(nil, "."); err != nil || !bytes.Equal(got, []byte{0x00}) {
		t.Fatalf("EncodeName(.) = (% x, %v), want (00, nil)", got, err)
	}

	// A trailing dot means the same thing as no trailing dot.
	want := testLabels("example", "com")
	for _, name := range []string{"example.com", "example.com."} {
		got, err := dnsx.EncodeName(nil, name)
		if err != nil {
			t.Fatalf("EncodeName(%q): %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("EncodeName(%q) = % x, want % x", name, got, want)
		}
	}

	// Appending: what is already in dst stays untouched, the name lands after it.
	prefix := []byte("PRE")
	wantAppend := append(append([]byte(nil), prefix...), testLabels("a", "b")...)
	got, err := dnsx.EncodeName(prefix, "a.b")
	if err != nil {
		t.Fatalf("EncodeName(dst, a.b): %v", err)
	}
	if !bytes.Equal(got, wantAppend) {
		t.Errorf("EncodeName(dst, a.b) = % x, want % x", got, wantAppend)
	}
}

func TestCh10_EncodeName_RejectsBadNamesAndLeavesDstAlone(t *testing.T) {
	testutil.NoLeaks(t)

	// 255 bytes of wire name (labels + length octets + terminating zero) is EXACTLY the limit and fits.
	exactly255 := strings.Repeat("a", 63) + "." +
		strings.Repeat("b", 63) + "." +
		strings.Repeat("c", 63) + "." +
		strings.Repeat("d", 61)
	got, err := dnsx.EncodeName(nil, exactly255)
	if err != nil {
		t.Fatalf("EncodeName(255-byte name): %v", err)
	}
	if len(got) != 255 {
		t.Errorf("EncodeName(255-byte name) produced %d bytes, want 255", len(got))
	}

	tooLong := strings.Repeat("a", 63) + "." +
		strings.Repeat("b", 63) + "." +
		strings.Repeat("c", 63) + "." +
		strings.Repeat("d", 62)

	cases := []struct {
		name string
		want error
	}{
		{strings.Repeat("a", 64), dnsx.ErrNameTooLong},
		{tooLong, dnsx.ErrNameTooLong},
		{"a..b", dnsx.ErrFormat},
		{".example.com", dnsx.ErrFormat},
		{"..", dnsx.ErrFormat},
		{"", dnsx.ErrFormat},
	}
	for _, tc := range cases {
		dst := []byte("keep me")
		_, err := dnsx.EncodeName(dst, tc.name)
		testutil.ExpectError(t, err)
		if !errors.Is(err, tc.want) {
			t.Errorf("EncodeName(%.40q) = %v, want an error wrapping %v", tc.name, err, tc.want)
		}
		if !bytes.Equal(dst, []byte("keep me")) {
			t.Errorf("EncodeName(%.40q) modified dst: % x", tc.name, dst)
		}
	}
}

func TestCh10_ParseName_DecodesNamesAndReportsTheEndOffset(t *testing.T) {
	testutil.NoLeaks(t)

	// The root name is "." and consumes one byte.
	if name, end, err := dnsx.ParseName([]byte{0x00}, 0); err != nil || name != "." || end != 1 {
		t.Fatalf("ParseName(root) = (%q, %d, %v), want (., 1, nil)", name, end, err)
	}

	// A name in the middle of a buffer: offset 12, like a real question name.
	buf := append(bytes.Repeat([]byte{0xAA}, 12), testLabels("example", "com")...)
	name, end, err := dnsx.ParseName(buf, 12)
	if err != nil {
		t.Fatalf("ParseName: %v", err)
	}
	if name != "example.com" || end != 12+13 {
		t.Fatalf("ParseName = (%q, %d), want (example.com, 25)", name, end)
	}

	// A decoded name longer than 255 bytes is malformed even though every label is legal.
	huge := testLabels(strings.Repeat("a", 63), strings.Repeat("b", 63), strings.Repeat("c", 63), strings.Repeat("d", 63))
	_, _, err = dnsx.ParseName(huge, 0)
	testutil.ExpectError(t, err)
	if !errors.Is(err, dnsx.ErrFormat) {
		t.Errorf("ParseName(257-byte name) = %v, want ErrFormat", err)
	}

	// Truncation (anywhere) and out-of-range offsets are ErrFormat, not panics.
	for _, tc := range []struct {
		desc   string
		b      []byte
		offset int
	}{
		{"label cut off", []byte{0x07, 'e'}, 0},
		{"no terminating zero", []byte{0x01, 'a'}, 0},
		{"empty buffer", nil, 0},
		{"offset past the end", []byte{0x00}, 1},
		{"negative offset", []byte{0x00}, -1},
	} {
		_, _, err := dnsx.ParseName(tc.b, tc.offset)
		testutil.ExpectError(t, err)
		if !errors.Is(err, dnsx.ErrFormat) {
			t.Errorf("%s: ParseName = %v, want ErrFormat", tc.desc, err)
		}
	}
}

func TestCh10_ParseName_FollowsCompressionPointers(t *testing.T) {
	testutil.NoLeaks(t)

	base := testLabels("www", "example", "com") // 17 bytes at offset 0
	if len(base) != 17 {
		t.Fatalf("test bug: base name is %d bytes, expected 17", len(base))
	}
	b := append([]byte(nil), base...)

	// A name that is nothing but a pointer: www.example.com, ending AFTER THE POINTER (2 bytes).
	b = append(b, 0xC0, 0x00)
	name, end, err := dnsx.ParseName(b, 17)
	if err != nil || name != "www.example.com" || end != 19 {
		t.Fatalf("ParseName(pointer) = (%q, %d, %v), want (www.example.com, 19, nil)", name, end, err)
	}

	// A label followed by a pointer: ftp + www.example.com.
	ftp := []byte{0x03, 'f', 't', 'p'}
	ftpOff := len(b)
	b = append(b, ftp...)
	b = append(b, 0xC0, 0x00)
	name, end, err = dnsx.ParseName(b, ftpOff)
	if err != nil || name != "ftp.www.example.com" || end != ftpOff+6 {
		t.Fatalf("ParseName(ftp + pointer) = (%q, %d, %v), want (ftp.www.example.com, %d, nil)", name, end, err, ftpOff+6)
	}

	// Pointer-to-pointer: the target name itself contains a pointer.
	mailOff := len(b)
	b = append(b, testLabels("mail")...)
	ptrOff := len(b)
	b = append(b, 0xC0, byte(mailOff))   // points at "mail"
	mixOff := len(b)
	b = append(b, 0x01, 'x', 0xC0, byte(ptrOff)) // "x" + pointer at the pointer

	name, end, err = dnsx.ParseName(b, mixOff)
	if err != nil || name != "x.mail" || end != mixOff+4 {
		t.Fatalf("ParseName(x + pointer-to-pointer) = (%q, %d, %v), want (x.mail, %d, nil)", name, end, err, mixOff+4)
	}
	name, end, err = dnsx.ParseName(b, ptrOff)
	if err != nil || name != "mail" || end != ptrOff+2 {
		t.Fatalf("ParseName(pointer at pointer) = (%q, %d, %v), want (mail, %d, nil)", name, end, err, ptrOff+2)
	}
}

func TestCh10_ParseName_RejectsLoopsAndBadPointers(t *testing.T) {
	testutil.NoLeaks(t)

	cases := []struct {
		desc string
		b    []byte
	}{
		{"self pointer", []byte{0xC0, 0x00}},
		{"two-pointer cycle", []byte{0xC0, 0x02, 0xC0, 0x00}},
		{"pointer past the end", []byte{0xC0, 0xFF}},
		{"truncated pointer", []byte{0xC0}},
		{"reserved label type 01", []byte{0x40}},
		{"reserved label type 10", []byte{0x80}},
	}
	for _, tc := range cases {
		_, _, err := dnsx.ParseName(tc.b, 0)
		testutil.ExpectError(t, err)
		if !errors.Is(err, dnsx.ErrFormat) {
			t.Errorf("%s: ParseName = %v, want ErrFormat", tc.desc, err)
		}
	}

	// The same failure inside a whole message: the answer's owner name points at itself.
	// Offset 12 + 17 bytes of question = 29, so the record starts at byte 29.
	selfLoop := testRecord([]byte{0xC0, 29}, dnsx.TypeA, dnsx.ClassIN, 300, []byte{1, 2, 3, 4})
	_, err := dnsx.ParseMessage(craftResponse(0xBEEF, respFlags, selfLoop))
	testutil.ExpectError(t, err)
	if !errors.Is(err, dnsx.ErrFormat) {
		t.Errorf("ParseMessage(self-referential answer name) = %v, want ErrFormat", err)
	}
}

func TestCh10_ParseMessage_ReadsAnAnswerWithACompressedName(t *testing.T) {
	testutil.NoLeaks(t)

	// A typical resolver response: the answer's owner name is not repeated — it is a two-byte pointer to the
	// question's example.com at offset 12.
	packet := craftResponse(0xBEEF, respFlags, testA(3600, [4]byte{93, 184, 216, 34}))

	m, err := dnsx.ParseMessage(packet)
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	if m.ID != 0xBEEF || m.Flags != respFlags {
		t.Errorf("header = (%#04x, %#04x), want (%#04x, %#04x)", m.ID, m.Flags, 0xBEEF, respFlags)
	}
	if len(m.Questions) != 1 || m.Questions[0] != (dnsx.Question{Name: "example.com", Type: dnsx.TypeA, Class: dnsx.ClassIN}) {
		t.Errorf("questions = %+v, want the example.com/A/IN question", m.Questions)
	}
	if len(m.Answers) != 1 {
		t.Fatalf("answers = %+v, want exactly one", m.Answers)
	}

	rr := m.Answers[0]
	if rr.Name != "example.com" || rr.Type != dnsx.TypeA || rr.Class != dnsx.ClassIN || rr.TTL != 3600 {
		t.Errorf("answer = %+v, want example.com A IN TTL=3600", rr)
	}
	ip, err := dnsx.RDataToIPv4(rr.RData)
	if err != nil || ip != "93.184.216.34" {
		t.Fatalf("RDataToIPv4(% x) = (%q, %v), want (93.184.216.34, nil)", rr.RData, ip, err)
	}
}

func TestCh10_ParseMessage_SkipsAuthorityAndAdditionalSections(t *testing.T) {
	testutil.NoLeaks(t)

	m, err := dnsx.ParseMessage(testResponseWithSOA())
	if err != nil {
		t.Fatalf("ParseMessage: %v", err)
	}
	if len(m.Answers) != 1 || m.Answers[0].Type != dnsx.TypeA {
		t.Fatalf("answers = %+v, want just the one A record", m.Answers)
	}
	ip, err := dnsx.RDataToIPv4(m.Answers[0].RData)
	if err != nil || ip != "9.9.9.9" {
		t.Fatalf("the A answer was lost among the skipped sections: (%q, %v)", ip, err)
	}
}

func TestCh10_ParseMessage_RejectsAbsurdSectionCounts(t *testing.T) {
	testutil.NoLeaks(t)

	// More questions than anyone legitimately sends: 32 is the ceiling. Checked before a single body byte is read.
	for _, tc := range []struct {
		desc string
		b    []byte
	}{
		{"QDCOUNT 33", testHeader(1, respFlags, 33, 0, 0, 0)},
		{"ANCOUNT 129", testHeader(1, respFlags, 0, 129, 0, 0)},
	} {
		_, err := dnsx.ParseMessage(tc.b)
		testutil.ExpectError(t, err)
		if !errors.Is(err, dnsx.ErrFormat) {
			t.Errorf("%s: ParseMessage = %v, want ErrFormat", tc.desc, err)
		}
	}
}

func TestCh10_ParseMessage_EveryTruncationIsErrFormat(t *testing.T) {
	testutil.NoLeaks(t)

	full := testResponseWithSOA()
	if _, err := dnsx.ParseMessage(full); err != nil {
		t.Fatalf("the untruncated message must parse: %v", err)
	}

	for cut := 0; cut < len(full); cut++ {
		m, err := dnsx.ParseMessage(full[:cut])
		if err == nil {
			t.Fatalf("a message truncated to %d of %d bytes parsed successfully: %+v", cut, len(full), m)
		}
		if !errors.Is(err, dnsx.ErrFormat) {
			t.Fatalf("truncated to %d bytes: err = %v, want ErrFormat", cut, err)
		}
	}
}

func TestCh10_RDataToIPv4_FormatsAndRejects(t *testing.T) {
	testutil.NoLeaks(t)

	cases := []struct {
		in   []byte
		want string
	}{
		{[]byte{93, 184, 216, 34}, "93.184.216.34"},
		{[]byte{0, 0, 0, 0}, "0.0.0.0"},
		{[]byte{255, 255, 255, 255}, "255.255.255.255"},
		{[]byte{127, 0, 0, 1}, "127.0.0.1"},
	}
	for _, tc := range cases {
		got, err := dnsx.RDataToIPv4(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("RDataToIPv4(% x) = (%q, %v), want (%q, nil)", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range [][]byte{nil, {}, {1}, {1, 2, 3}, {1, 2, 3, 4, 5}} {
		_, err := dnsx.RDataToIPv4(bad)
		testutil.ExpectError(t, err)
		if !errors.Is(err, dnsx.ErrFormat) {
			t.Errorf("RDataToIPv4(% x) = %v, want an error wrapping ErrFormat", bad, err)
		}
	}
}

func TestCh10_ResolveA_ReturnsAddressesAndSendsTheGoldenQuery(t *testing.T) {
	testutil.NoLeaks(t)

	tr := &scriptedTransport{reply: craftResponse(0xBEEF, respFlags, testA(300, [4]byte{93, 184, 216, 34}))}
	got, err := dnsx.ResolveA(tr, 0xBEEF, "example.com")
	if err != nil {
		t.Fatalf("ResolveA: %v", err)
	}
	if want := []string{"93.184.216.34"}; !slices.Equal(got, want) {
		t.Fatalf("ResolveA = %v, want %v", got, want)
	}
	if tr.calls != 1 {
		t.Errorf("RoundTrip called %d times, want exactly 1", tr.calls)
	}
	// The query that went on the wire must be the exact hand-computed golden query.
	if want := queryWithID(0xBEEF); !bytes.Equal(tr.gotQuery, want) {
		t.Fatalf("the query on the wire is wrong:\ngot  % x\nwant % x", tr.gotQuery, want)
	}
}

func TestCh10_ResolveA_RejectsIDMismatchQueryEchoesAndTransportErrors(t *testing.T) {
	testutil.NoLeaks(t)

	// A response with somebody else's ID — over UDP this is the crossed-wires (or spoofed) case.
	mismatched := &scriptedTransport{reply: craftResponse(0x1111, respFlags, testA(300, [4]byte{1, 1, 1, 1}))}
	_, err := dnsx.ResolveA(mismatched, 0x2222, "example.com")
	testutil.ExpectError(t, err)

	// The query echoed back: same ID, QR clear, so it is NOT an answer.
	echo := &scriptedTransport{reply: queryWithID(0xBEEF)}
	_, err = dnsx.ResolveA(echo, 0xBEEF, "example.com")
	testutil.ExpectError(t, err)

	// The transport itself fails (dead socket, timeout): the error must surface unchanged.
	dead := &scriptedTransport{err: errScriptedTransport}
	_, err = dnsx.ResolveA(dead, 0x0001, "example.com")
	testutil.ExpectError(t, err)
	if !errors.Is(err, errScriptedTransport) {
		t.Fatalf("ResolveA(dead transport) = %v, want the transport error", err)
	}
}

func TestCh10_ResolveA_MapsRcodesToErrNXDomainAndRcodeError(t *testing.T) {
	testutil.NoLeaks(t)

	// rcode 3 = NXDOMAIN: the domain exists, the name does not. The sentinel exists for errors.Is.
	nx := &scriptedTransport{reply: craftResponse(0xBEEF, respFlags|3, testA(300, [4]byte{1, 2, 3, 4}))}
	got, err := dnsx.ResolveA(nx, 0xBEEF, "example.com")
	testutil.ExpectError(t, err)
	if !errors.Is(err, dnsx.ErrNXDomain) {
		t.Fatalf("rcode 3: err = %v, want ErrNXDomain", err)
	}
	if got != nil {
		t.Errorf("rcode 3 returned addresses %v; a failure must not carry answers", got)
	}

	// Everything else non-zero is an *RcodeError carrying the numeric code: 2 = SERVFAIL, 5 = REFUSED.
	for _, code := range []uint16{2, 5} {
		tr := &scriptedTransport{reply: craftResponse(0xBEEF, respFlags|code, testA(300, [4]byte{1, 2, 3, 4}))}
		_, err := dnsx.ResolveA(tr, 0xBEEF, "example.com")
		testutil.ExpectError(t, err)
		var rcodeErr *dnsx.RcodeError
		if !errors.As(err, &rcodeErr) {
			t.Fatalf("rcode %d: err = %v (%T), want *RcodeError", code, err, err)
		}
		if rcodeErr.Rcode != code {
			t.Errorf("rcode %d: RcodeError.Rcode = %d", code, rcodeErr.Rcode)
		}
		if !strings.Contains(rcodeErr.Error(), fmt.Sprint(code)) {
			t.Errorf("rcode %d: Error() = %q, want it to mention the numeric code", code, rcodeErr.Error())
		}
	}
}

func TestCh10_ResolveA_IgnoresNonAAnswersAndKeepsOrder(t *testing.T) {
	testutil.NoLeaks(t)

	ip6 := []byte{0x20, 0x01, 0x0d, 0xb8, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}
	answers := [][]byte{
		testA(300, [4]byte{1, 1, 1, 1}),
		testRecord(ptrToQuestion, dnsx.TypeAAAA, dnsx.ClassIN, 300, ip6),
		testA(300, [4]byte{2, 2, 2, 2}),
		testRecord(ptrToQuestion, dnsx.TypeCNAME, dnsx.ClassIN, 300, testLabels("www", "example", "com")),
		testRecord(ptrToQuestion, dnsx.TypeA, dnsx.ClassIN, 300, []byte{3, 3, 3}), // malformed: 3 bytes
		testRecord(ptrToQuestion, dnsx.TypeA, dnsx.ClassIN, 300, []byte{4, 4, 4, 4}),
		testRecord(ptrToQuestion, dnsx.TypeTXT, dnsx.ClassIN, 60, []byte{5, 'h', 'e', 'l', 'l', 'o'}),
	}
	tr := &scriptedTransport{reply: craftResponse(0xBEEF, respFlags, answers...)}

	got, err := dnsx.ResolveA(tr, 0xBEEF, "example.com")
	if err != nil {
		t.Fatalf("ResolveA: %v", err)
	}
	want := []string{"1.1.1.1", "2.2.2.2", "4.4.4.4"}
	if !slices.Equal(got, want) {
		t.Fatalf("ResolveA = %v, want %v (AAAA/CNAME/TXT ignored, malformed A skipped, answer order kept)", got, want)
	}
}
