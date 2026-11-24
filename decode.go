package internetdata

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Reads the JSON answer a call returns. Anything but a 2xx is classified by its
// status before the body is decoded, so a proxy's HTML 401 is unauthorized and
// sent once. A 2xx that is not the answer - a proxy's HTML page, a cut-off or
// empty body, `null`, or one without a member the answer requires - is a server
// error carrying that status, retried like a 5xx. Through v2.6.0 an HTML page
// was a bad_request sent once, a cut-off body a network error with no status,
// and `{}` an empty catalog handed back as the answer.
func decodeAnswer[T any](res *http.Response, err error) (*T, error) {
	if err != nil {
		return nil, errorFromTransport(err)
	}
	body, err := readBody(res)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, errorFromResponse(res.StatusCode, res.Header, body)
	}
	answer, ok := decodeComplete[T](body)
	if !ok {
		return nil, unreadable(res.StatusCode, "the response was not the answer this call returns")
	}
	return answer, nil
}

// Decodes raw as a T holding every member T requires. The answer is decoded
// from the value complete hands back, each date-time in the spelling time.Time
// reads, so json.Number keeps every digit of a number on the way.
func decodeComplete[T any](raw []byte) (*T, bool) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, false
	}
	value, ok := complete(reflect.TypeFor[T](), value)
	if !ok {
		return nil, false
	}
	spelled, err := json.Marshal(value)
	var answer T
	if err != nil || json.Unmarshal(spelled, &answer) != nil {
		return nil, false
	}
	return &answer, true
}

var (
	unmarshaler = reflect.TypeFor[json.Unmarshaler]()
	timeType    = reflect.TypeFor[time.Time]()
)

// The members the spec requires that overlay.yaml generates as pointers, so a
// v2 minor keeps their public types, by the Go type holding them. Each is still
// refused absent or null, as any required member is.
var heldRequired = map[reflect.Type][]string{
	reflect.TypeFor[Asn](): {"country", "rpki_status"},
}

// The members the spec gives format: date that overlay.yaml generates as
// strings, so a v2 minor keeps their public types, by the Go type holding them.
// Each is refused unless it is RFC 3339's full-date on a real day, the check
// the types.Date it would otherwise be makes.
var heldDates = map[reflect.Type][]string{
	reflect.TypeFor[ClassDetail]():       {"last_seen"},
	reflect.TypeFor[ProxyDetail]():       {"first_seen", "last_seen"},
	reflect.TypeFor[PubproxyDetail]():    {"last_seen"},
	reflect.TypeFor[BotDetail]():         {"first_seen", "last_seen"},
	reflect.TypeFor[SSHDetail]():         {"first_seen", "last_seen"},
	reflect.TypeFor[SMTPDetail]():        {"first_seen", "last_seen"},
	reflect.TypeFor[WebDetail]():         {"first_seen", "last_seen"},
	reflect.TypeFor[DNSResolverDetail](): {"first_seen", "last_seen"},
	reflect.TypeFor[BitTorrentDetail]():  {"first_seen", "last_seen"},
	reflect.TypeFor[AsnPrefix]():         {"first_seen"},
	reflect.TypeFor[DNSAddressRecord]():  {"last_seen"},
	reflect.TypeFor[DNSMxRecord]():       {"last_seen"},
	reflect.TypeFor[DNSNsRecord]():       {"last_seen"},
	reflect.TypeFor[DNSCnameRecord]():    {"last_seen"},
	reflect.TypeFor[DisposableDetail]():  {"first_seen", "last_seen"},
}

var fullDateForm = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// Reports whether raw is RFC 3339's full-date on a day its month has.
func fullDate(raw any) bool {
	value, ok := raw.(string)
	if !ok || !fullDateForm.MatchString(value) {
		return false
	}
	_, err := time.Parse(time.DateOnly, value)
	return err == nil
}

// RFC 3339's date-time, its T and Z in either case. time.Time reads a one-digit
// hour and a comma before the fraction too, and refuses a lowercase t or z, so
// through v2.8.3 the first two were dates and the last failed the call.
var rfc3339Form = regexp.MustCompile(
	`^[0-9]{4}-[0-9]{2}-[0-9]{2}[Tt][0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?([Zz]|[+-][0-9]{2}:[0-9]{2})$`)

// Hands back a date-time in the spelling time.Time reads, capital T and Z, and
// reports whether it is RFC 3339's form with a zone it allows: Z, or an offset
// of at most 23 hours and 59 minutes. time.Time reads +24:00 and +05:60 too,
// so through v2.8.2 either was a date a day or an hour off. The day, hour,
// minute and second that exist are left to time.Time, which refuses the rest.
func rfc3339(value string) (string, bool) {
	if !rfc3339Form.MatchString(value) {
		return value, false
	}
	value = value[:10] + "T" + value[11:]
	if last := len(value) - 1; value[last] == 'z' {
		value = value[:last] + "Z"
	}
	if strings.HasSuffix(value, "Z") {
		return value, true
	}
	offset := value[len(value)-len("+00:00"):]
	return value, offset[1:3] <= "23" && offset[4:6] <= "59"
}

// Reports whether raw, a decoded JSON value, carries every member t requires,
// at every depth, and hands it back with each date-time in the spelling
// time.Time reads. The generated types spell a member the spec requires and
// does not allow to be null as neither a pointer nor omitempty, so that is the
// rule: present, and not null. json.Unmarshal checks neither, and reads a
// missing or null member as its zero value.
func complete(t reflect.Type, raw any) (any, bool) {
	if raw == nil {
		return raw, t.Kind() == reflect.Pointer || t.Kind() == reflect.Interface
	}
	if t.Kind() == reflect.Pointer {
		return complete(t.Elem(), raw)
	}
	if t == timeType {
		value, ok := raw.(string)
		if !ok {
			return raw, false
		}
		return rfc3339(value)
	}
	if reflect.PointerTo(t).Implements(unmarshaler) {
		return raw, true
	}
	switch t.Kind() {
	case reflect.Struct:
		object, ok := raw.(map[string]any)
		if !ok {
			return raw, false
		}
		for i := range t.NumField() {
			field := t.Field(i)
			name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
			if !field.IsExported() || name == "" || name == "-" {
				continue
			}
			value, present := object[name]
			required := field.Type.Kind() != reflect.Pointer && !strings.Contains(options, "omitempty")
			held := slices.Contains(heldRequired[t], name)
			if (required || held) && !present || held && value == nil {
				return raw, false
			}
			if present && value != nil && slices.Contains(heldDates[t], name) && !fullDate(value) {
				return raw, false
			}
			if present {
				if object[name], ok = complete(field.Type, value); !ok {
					return raw, false
				}
			}
		}
	case reflect.Slice:
		items, ok := raw.([]any)
		if !ok {
			return raw, false
		}
		for i, item := range items {
			if items[i], ok = complete(t.Elem(), item); !ok {
				return raw, false
			}
		}
	case reflect.Map:
		entries, ok := raw.(map[string]any)
		if !ok {
			return raw, false
		}
		for key, entry := range entries {
			if entries[key], ok = complete(t.Elem(), entry); !ok {
				return raw, false
			}
		}
	}
	return raw, true
}
