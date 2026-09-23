package address

import (
	"strings"
	"testing"
)

func validFields() Fields {
	return Fields{Recipient: "Иван", Phone: "+7 (999) 123-45-67", Country: "Россия", City: "Москва", StreetHouse: "Лесная, 1"}
}
func TestFieldsNormalizationAndBoundaries(t *testing.T) {
	f := validFields()
	f.Recipient = "  Иван  "
	f.Comment = " text\n\tline "
	got, e := NewFields(f)
	if e != nil || got.Recipient != "Иван" || got.Comment != "text\n\tline" {
		t.Fatal(got, e)
	}
	for _, change := range []func(*Fields){
		func(f *Fields) { f.Recipient = strings.Repeat("я", 121) }, func(f *Fields) { f.Recipient = strings.Repeat(" ", 481) }, func(f *Fields) { f.Recipient = "\tИван" }, func(f *Fields) { f.City = "" }, func(f *Fields) { f.Country = "\xff" }, func(f *Fields) { f.Comment = "a\rb" }, func(f *Fields) { f.Phone = "123456" }, func(f *Fields) { f.Phone = "1234567890123456" }, func(f *Fields) { f.Phone = "１２３４５６７" }, func(f *Fields) { f.Phone = "1234567x" }, func(f *Fields) { f.Phone = strings.Repeat(" ", 33) }, func(f *Fields) { f.PostalCode = strings.Repeat("x", 21) }, func(f *Fields) { f.Apartment = strings.Repeat("x", 41) },
	} {
		v := validFields()
		change(&v)
		if _, e := NewFields(v); e == nil {
			t.Fatalf("accepted invalid fields: %#v", v)
		}
	}
	f = validFields()
	f.Phone = "\u00a01234567"
	if _, e := NewFields(f); e == nil {
		t.Fatal("non-ASCII phone whitespace accepted")
	}
	f = validFields()
	f.Country = strings.Repeat("я", 80)
	f.Comment = strings.Repeat("😀", 500)
	if _, e := NewFields(f); e != nil {
		t.Fatal(e)
	}
}
func TestIDVersionAndBookValidation(t *testing.T) {
	if _, e := NewID(make([]byte, 16)); e == nil {
		t.Fatal("zero id accepted")
	}
	if _, e := NewID([]byte{1}); e == nil {
		t.Fatal("short id accepted")
	}
	if ValidVersion(0) || ValidVersion(^uint64(0)) {
		t.Fatal("invalid version accepted")
	}
	b := Book{Version: 1}
	b.SubjectID[0] = 1
	a := Address{ID: ID{1}, Fields: validFields(), IsDefault: true}
	b.Addresses = []Address{a}
	if b.Validate() != nil {
		t.Fatal("valid book rejected")
	}
	b.Addresses = append(b.Addresses, a)
	if b.Validate() == nil {
		t.Fatal("duplicate accepted")
	}
}
