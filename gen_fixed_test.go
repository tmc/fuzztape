package fuzztape

import (
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
)

func TestGenFixedWidth(t *testing.T) {
	draw := func(g func(*Tape) any) func([]byte) any {
		return func(b []byte) any { return g(New(b)) }
	}
	i8 := draw(func(t *Tape) any { return Int8()(t) })
	i16 := draw(func(t *Tape) any { return Int16()(t) })
	i32 := draw(func(t *Tape) any { return Int32()(t) })
	i64 := draw(func(t *Tape) any { return Int64()(t) })
	u8 := draw(func(t *Tape) any { return Uint8()(t) })
	u16 := draw(func(t *Tape) any { return Uint16()(t) })
	u32 := draw(func(t *Tape) any { return Uint32()(t) })
	u64 := draw(func(t *Tape) any { return Uint64()(t) })
	tests := []struct {
		name  string
		gen   func([]byte) any
		input []byte
		want  any
	}{
		{"int64 zero tape", i64, nil, int64(0)},
		{"int64 one", i64, []byte{0x10}, int64(1)},
		{"int64 minus one", i64, []byte{0x20}, int64(-1)},
		{"int64 min", i64, []byte{0x40}, int64(math.MinInt64)},
		{"int64 max", i64, []byte{0x60}, int64(math.MaxInt64)},
		{"int64 min+1", i64, []byte{0x70}, int64(math.MinInt64 + 1)},
		{"int64 max-1", i64, []byte{0x71}, int64(math.MaxInt64 - 1)},
		{"int64 width-1", i64, []byte{0x80}, int64(63)},
		{"int64 width", i64, []byte{0x81}, int64(64)},
		{"int64 width+1", i64, []byte{0x82}, int64(65)},
		{"int64 2^40", i64, []byte{0x90, 40}, int64(1 << 40)},
		{"int64 2^40-1", i64, []byte{0x91, 40}, int64(1<<40 - 1)},
		{"int64 2^40+1", i64, []byte{0x92, 40}, int64(1<<40 + 1)},
		{"int64 -2^40", i64, []byte{0x93, 40}, int64(-1 << 40)},
		{"int64 -2^40+1", i64, []byte{0x94, 40}, int64(-1<<40 + 1)},
		{"int64 -2^40-1", i64, []byte{0x95, 40}, int64(-1<<40 - 1)},
		{"int64 2^63 wraps to min", i64, []byte{0x90, 63}, int64(math.MinInt64)},
		{"int64 uniform", i64, []byte{0xa0, 1, 2, 3, 4, 5, 6, 7, 8}, int64(0x0807060504030201)},
		{"int64 minus one, second bucket", i64, []byte{0x3f}, int64(-1)},
		{"int64 min, second bucket", i64, []byte{0x5f}, int64(math.MinInt64)},
		{"int8 min", i8, []byte{0x40}, int8(math.MinInt8)},
		{"int8 max", i8, []byte{0x60}, int8(math.MaxInt8)},
		{"int8 width", i8, []byte{0x81}, int8(8)},
		{"int8 exponent wraps", i8, []byte{0x90, 9}, int8(2)},
		{"int8 uniform", i8, []byte{0xff, 0xfe}, int8(-2)},
		{"int16 min", i16, []byte{0x40}, int16(math.MinInt16)},
		{"int16 uniform", i16, []byte{0xa0, 0x34, 0x12}, int16(0x1234)},
		{"int32 minus one", i32, []byte{0x20}, int32(-1)},
		{"int32 width", i32, []byte{0x81}, int32(32)},
		{"uint8 max", u8, []byte{0x20}, uint8(math.MaxUint8)},
		{"uint8 top bit", u8, []byte{0x40}, uint8(0x80)},
		{"uint8 max-1", u8, []byte{0x70}, uint8(math.MaxUint8 - 1)},
		{"uint8 two", u8, []byte{0x71}, uint8(2)},
		{"uint16 max int16", u16, []byte{0x60}, uint16(math.MaxInt16)},
		{"uint32 width+1", u32, []byte{0x82}, uint32(33)},
		{"uint64 zero tape", u64, nil, uint64(0)},
		{"uint64 max", u64, []byte{0x20}, uint64(math.MaxUint64)},
		{"uint64 width", u64, []byte{0x81}, uint64(64)},
		{"uint64 2^6", u64, []byte{0x90, 6}, uint64(64)},
		{"uint64 unsigned has no negatives", u64, []byte{0x93, 6}, uint64(64)},
		{"uint64 2^0-1", u64, []byte{0x91, 0}, uint64(0)},
		{"uint64 short tape zero-fills", u64, []byte{0xa0, 0xff}, uint64(0xff)},
	}
	for _, tt := range tests {
		if got := tt.gen(tt.input); got != tt.want {
			t.Errorf("%s: got %v (%T), want %v (%T)", tt.name, got, got, tt.want, tt.want)
		}
	}
}

// TestGenFixedWidthReachesBoundaries is the reason the fixed-width
// generators exist. Uniform draws essentially never produce MinInt64
// followed by -1, the one division that overflows, or a shift count
// equal to the width, where Go yields 0 and other languages (C, and
// the bit-vector models a differential test compares against) do
// something else. Within 1,000 draws from random tapes both must occur.
func TestGenFixedWidthReachesBoundaries(t *testing.T) {
	for seed := range uint64(10) {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(seed, 0))
			data := make([]byte, 64<<10)
			for i := range data {
				data[i] = byte(r.Uint32())
			}
			tape := New(data)
			i64, u64 := Int64(), Uint64()
			pair, shift := false, false
			for range 1000 {
				x, y := i64(tape), i64(tape)
				pair = pair || x == math.MinInt64 && y == -1
				shift = shift || u64(tape) == 64
			}
			if tape.Done() {
				t.Fatal("tape ran out; the draws were not from random bytes")
			}
			if !pair {
				t.Error("1000 pairs of Int64 draws never produced (MinInt64, -1)")
			}
			if !shift {
				t.Error("1000 Uint64 draws never produced the shift count 64")
			}
		})
	}
}
