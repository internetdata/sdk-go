// Package middleware holds the framework-agnostic half of a web middleware:
// resolve a client address, classify it, and decide whether to block.
//
// A framework adapter - sdk-go-nethttp, sdk-go-gin - keeps only the parts that
// are genuinely framework-shaped and shares everything here, so the shared
// conformance corpus is asserted once for Go rather than once per framework.
package middleware

import (
	"fmt"
	"reflect"
	"strings"

	internetdata "github.com/internetdata/sdk-go/v2"
	"github.com/internetdata/sdk-go/v2/internal/held"
)

// BlockCondition is what makes a request worth blocking, written in the shape
// of a Result and keyed by the wire's own names.
//
// Only the members you name are considered, and they must all hold. A member
// set to false or nil is ignored entirely - a condition states the positive
// signals you act on, so there is no way to write "block when this is false",
// which would otherwise read as blocking everybody.
//
//	middleware.BlockCondition{"location": middleware.BlockCondition{"country": middleware.AnyOf("CU", "IR", "KP")}}
//	middleware.BlockCondition{"asn": middleware.BlockCondition{"type": "hosting"}}
//	middleware.BlockCondition{"is_vpn": true, "vpn": middleware.BlockCondition{"provider": "nordvpn"}}
//	middleware.BlockCondition{"resproxy": middleware.BlockCondition{"hits": middleware.Gte(5)}}
//
// A value may be a scalar (equality, strings without regard to case), an AnyOf,
// a Bound, or a nested BlockCondition. Against a member the answer holds as a
// list, it matches when any one element does:
//
//	middleware.BlockCondition{"pubproxy": middleware.BlockCondition{"protocols": "socks5"}}
//	middleware.BlockCondition{"web": middleware.BlockCondition{"port": 443, "server": "nginx"}}
type BlockCondition map[string]any

// Conditions is a list of conditions, any one of which blocking is enough.
type Conditions []BlockCondition

// Bound compares a numeric member. Every field set must hold, so two of them
// are a range: Gte(5).Lt(100).
type Bound struct {
	GteV *float64
	GtV  *float64
	LteV *float64
	LtV  *float64
}

// Gte bounds a member at or above v.
func Gte(v float64) Bound { return Bound{GteV: &v} }

// Gt bounds a member strictly above v.
func Gt(v float64) Bound { return Bound{GtV: &v} }

// Lte bounds a member at or below v.
func Lte(v float64) Bound { return Bound{LteV: &v} }

// Lt bounds a member strictly below v.
func Lt(v float64) Bound { return Bound{LtV: &v} }

// Gte narrows an existing bound, so Gte(5).Lt(100) is a range.
func (b Bound) Gte(v float64) Bound { b.GteV = &v; return b }

// Gt narrows an existing bound.
func (b Bound) Gt(v float64) Bound { b.GtV = &v; return b }

// Lte narrows an existing bound.
func (b Bound) Lte(v float64) Bound { b.LteV = &v; return b }

// Lt narrows an existing bound.
func (b Bound) Lt(v float64) Bound { b.LtV = &v; return b }

// AnyOf matches a member equal to any one of these, for an enum-like field.
func AnyOf(values ...any) anyOf { return anyOf(values) }

type anyOf []any

// Matches reports whether an answer satisfies the condition and should
// therefore be blocked.
func Matches(conditions Conditions, result *internetdata.Result) bool {
	value := reflect.ValueOf(result).Elem()
	unserved := unservedBy(result)
	for _, one := range conditions {
		if matchesStruct(one, value, "", unserved) {
			return true
		}
	}
	return false
}

// The wire paths the answer left out although the member's Go type is not
// optional (is_bot on Unlimited), which are absent whatever the value reads.
func unservedBy(result *internetdata.Result) map[string]bool {
	paths := map[string]bool{}
	for _, path := range held.Unserved(result) {
		paths[path] = true
	}
	return paths
}

// MissingMembers names the members a condition asks about that this answer did
// not carry: a top-level member, or a paid one inside an object every plan gets,
// by its path (location.city).
//
// A field your plan does not include is absent rather than false, so a
// condition naming one can never match and the block would silently never fire.
// An empty object is a real answer, meaning the flag above it is false, so
// nothing inside one is reported: vpn present but {} is not a plan gap. A
// populated object carries every member the plan includes, so a member missing
// there is one.
//
// A member the answer left out is absent whatever its Go type reads: is_bot and
// the seven service flags, and asn.type, keep a bool and a string, and read
// false and empty on Unlimited.
//
// A locally answered bogon needs no special case: it is synthesized in the
// widest shape, so every member is present and nothing reads as missing.
func MissingMembers(conditions Conditions, result *internetdata.Result) []string {
	value := reflect.ValueOf(result).Elem()
	unserved := unservedBy(result)
	seen := map[string]bool{}
	var missing []string
	for _, one := range conditions {
		collectMissing(one, value, "", unserved, seen, &missing)
	}
	return missing
}

// Validate refuses a condition that constrains nothing.
//
// Ignoring false means BlockCondition{"is_vpn": false} and an empty one have no
// terms left to satisfy, so they would match every answer and block all
// traffic. Nobody writes that on purpose, and failing when the middleware is
// built beats discovering it in production.
func Validate(conditions Conditions) error {
	if len(conditions) == 0 {
		return nil
	}
	for _, one := range conditions {
		if ConstraintCount(one) == 0 {
			return fmt.Errorf(
				"internetdata: block condition constrains nothing, which would block every "+
					"request; a member set to false or nil is ignored, so state the positive "+
					"signals you act on (got %v)", map[string]any(one))
		}
	}
	return nil
}

// ConstraintCount reports how many leaf constraints a condition carries.
func ConstraintCount(condition any) int {
	switch v := condition.(type) {
	case nil:
		return 0
	case bool:
		if !v {
			return 0
		}
		return 1
	case BlockCondition:
		n := 0
		for _, entry := range v {
			n += ConstraintCount(entry)
		}
		return n
	case Conditions:
		n := 0
		for _, entry := range v {
			n += ConstraintCount(entry)
		}
		return n
	case anyOf:
		return len(v)
	default:
		return 1
	}
}

func collectMissing(condition BlockCondition, value reflect.Value, prefix string, unserved, seen map[string]bool,
	missing *[]string) {
	for member, want := range condition {
		if ConstraintCount(want) == 0 {
			continue
		}
		path := prefix + member
		field, ok := fieldFor(value, member)
		if !ok || isAbsent(field) || unserved[path] {
			if !seen[path] {
				seen[path] = true
				*missing = append(*missing, path)
			}
			continue
		}
		nested, isObject := want.(BlockCondition)
		got := deref(field)
		if isObject && got.Kind() == reflect.Struct && !isEmptyObject(got) {
			collectMissing(nested, got, path+".", unserved, seen, missing)
		}
	}
}

func matchesStruct(condition BlockCondition, value reflect.Value, prefix string, unserved map[string]bool) bool {
	for member, want := range condition {
		if ConstraintCount(want) == 0 {
			continue
		}
		field, ok := fieldFor(value, member)
		if !ok || unserved[prefix+member] || !matchesValue(want, deref(field), prefix+member+".", unserved) {
			return false
		}
	}
	return true
}

// An ABSENT member arrives here as an invalid reflect.Value, because deref of a
// nil pointer yields one. Every branch below must therefore reject a value
// whose Kind does not match, which is what makes "not in your plan" fail a
// match rather than pass it. A new branch that skips that check would silently
// let an unserved member block a request.
//
// A member the answer holds as a list (web's rows, pubproxy.protocols) matches
// when any one element does, so an object term must hold within one element.
// Through v2.16.2 the term was compared with the list itself and never matched.
func matchesValue(want any, got reflect.Value, prefix string, unserved map[string]bool) bool {
	if got.Kind() == reflect.Slice {
		for i := range got.Len() {
			if matchesValue(want, deref(got.Index(i)), prefix, unserved) {
				return true
			}
		}
		return false
	}
	switch w := want.(type) {
	case anyOf:
		for _, entry := range w {
			if matchesValue(entry, got, prefix, unserved) {
				return true
			}
		}
		return false
	case Bound:
		return matchesBound(w, got)
	case BlockCondition:
		if got.Kind() != reflect.Struct {
			return false
		}
		return matchesStruct(w, got, prefix, unserved)
	case string:
		// Providers are lowercase slugs and countries uppercase codes on the
		// wire, and a caller should not have to know which, so a string
		// compares without case.
		return got.Kind() == reflect.String && strings.EqualFold(w, got.String())
	case bool:
		return got.Kind() == reflect.Bool && got.Bool() == w
	default:
		if n, ok := asFloat(reflect.ValueOf(want)); ok {
			m, ok := asFloat(got)
			return ok && n == m
		}
		return false
	}
}

func matchesBound(bound Bound, got reflect.Value) bool {
	n, ok := asFloat(got)
	if !ok {
		return false
	}
	if bound.GteV != nil && n < *bound.GteV {
		return false
	}
	if bound.GtV != nil && n <= *bound.GtV {
		return false
	}
	if bound.LteV != nil && n > *bound.LteV {
		return false
	}
	if bound.LtV != nil && n >= *bound.LtV {
		return false
	}
	return true
}

// Looked up by JSON TAG rather than by field name, so a condition is written in
// the wire's vocabulary - the same one the shared corpus and the API docs use -
// and a Go field rename cannot silently change what a caller may write.
func fieldFor(value reflect.Value, member string) (reflect.Value, bool) {
	if value.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	t := value.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			if found, ok := fieldFor(value.Field(i), member); ok {
				return found, true
			}
			continue
		}
		if tagName(f.Tag.Get("json")) == member {
			return value.Field(i), true
		}
	}
	return reflect.Value{}, false
}

func tagName(tag string) string {
	name, _, _ := strings.Cut(tag, ",")
	return name
}

func isAbsent(v reflect.Value) bool {
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// An object with no member present, which on the wire is {}. A member that is
// not a pointer is one the spec requires, present in every decoded answer.
func isEmptyObject(v reflect.Value) bool {
	for i := 0; i < v.NumField(); i++ {
		if !isAbsent(v.Field(i)) {
			return false
		}
	}
	return true
}

func deref(v reflect.Value) reflect.Value {
	if v.Kind() == reflect.Pointer {
		return v.Elem()
	}
	return v
}

func asFloat(v reflect.Value) (float64, bool) {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(v.Uint()), true
	case reflect.Float32, reflect.Float64:
		return v.Float(), true
	default:
		return 0, false
	}
}
