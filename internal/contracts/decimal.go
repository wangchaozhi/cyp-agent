package contracts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

const (
	maxDecimalDigits = 10_000
	maxDecimalScale  = 10_000
)

var (
	errEmptyDecimal     = errors.New("decimal is empty")
	errInvalidDecimal   = errors.New("invalid decimal")
	errDecimalTooLarge  = errors.New("decimal exceeds supported precision")
	errDivisionByZero   = errors.New("decimal division by zero")
	errInvalidJSONValue = errors.New("decimal JSON value must be a string or number")
)

// Decimal is an immutable, exact base-10 number. Its zero value is the number
// zero. JSON always encodes a Decimal as a string so money never passes through
// float64; decoding accepts either a JSON string or a JSON number for backward
// compatibility with JSON and TypeScript clients.
type Decimal struct {
	coefficient  *big.Int
	scale        int32
	negativeZero bool
}

// RoundingMode controls the result of QuoScale.
type RoundingMode uint8

const (
	// RoundDown rounds toward zero, matching decimal.ROUND_DOWN.
	RoundDown RoundingMode = iota
	RoundHalfUp
	RoundHalfEven
)

func Zero() Decimal { return Decimal{} }

func NewDecimalFromInt64(value int64) Decimal {
	if value == 0 {
		return Zero()
	}
	return Decimal{coefficient: big.NewInt(value)}
}

func MustDecimal(text string) Decimal {
	d, err := ParseDecimal(text)
	if err != nil {
		panic(err)
	}
	return d
}

// ParseDecimal accepts ordinary and exponent notation, but never accepts
// NaN, Infinity, hexadecimal values, or locale-specific separators.
func ParseDecimal(text string) (Decimal, error) {
	original := text
	text = strings.TrimSpace(text)
	if text == "" {
		return Decimal{}, errEmptyDecimal
	}
	if len(text) > maxDecimalDigits+32 {
		return Decimal{}, fmt.Errorf("%w: %q", errDecimalTooLarge, abbreviate(original))
	}

	negative := false
	if text[0] == '+' || text[0] == '-' {
		negative = text[0] == '-'
		text = text[1:]
		if text == "" {
			return Decimal{}, fmt.Errorf("%w: %q", errInvalidDecimal, original)
		}
	}

	mantissa := text
	exponent := int64(0)
	if index := strings.IndexAny(text, "eE"); index >= 0 {
		if strings.ContainsAny(text[index+1:], "eE") {
			return Decimal{}, fmt.Errorf("%w: %q", errInvalidDecimal, original)
		}
		mantissa = text[:index]
		exponentText := text[index+1:]
		if exponentText == "" {
			return Decimal{}, fmt.Errorf("%w: %q", errInvalidDecimal, original)
		}
		var err error
		exponent, err = strconv.ParseInt(exponentText, 10, 32)
		if err != nil || exponent > maxDecimalScale || exponent < -maxDecimalScale {
			return Decimal{}, fmt.Errorf("%w: exponent in %q", errDecimalTooLarge, original)
		}
	}

	if mantissa == "" || strings.Count(mantissa, ".") > 1 {
		return Decimal{}, fmt.Errorf("%w: %q", errInvalidDecimal, original)
	}
	integerPart, fractionalPart := mantissa, ""
	if index := strings.IndexByte(mantissa, '.'); index >= 0 {
		integerPart = mantissa[:index]
		fractionalPart = mantissa[index+1:]
	}
	if integerPart == "" && fractionalPart == "" {
		return Decimal{}, fmt.Errorf("%w: %q", errInvalidDecimal, original)
	}
	digits := integerPart + fractionalPart
	if digits == "" || !asciiDigits(digits) {
		return Decimal{}, fmt.Errorf("%w: %q", errInvalidDecimal, original)
	}

	scale := int64(len(fractionalPart)) - exponent
	if scale < 0 {
		zeros := -scale
		if int64(len(digits))+zeros > maxDecimalDigits {
			return Decimal{}, fmt.Errorf("%w: %q", errDecimalTooLarge, abbreviate(original))
		}
		digits += strings.Repeat("0", int(zeros))
		scale = 0
	}
	if scale > maxDecimalScale || len(digits) > maxDecimalDigits {
		return Decimal{}, fmt.Errorf("%w: %q", errDecimalTooLarge, abbreviate(original))
	}

	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return Decimal{scale: int32(scale), negativeZero: negative}, nil
	}
	coefficient, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return Decimal{}, fmt.Errorf("%w: %q", errInvalidDecimal, original)
	}
	if negative {
		coefficient.Neg(coefficient)
	}
	return Decimal{coefficient: coefficient, scale: int32(scale)}, nil
}

func asciiDigits(text string) bool {
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func abbreviate(text string) string {
	if len(text) <= 80 {
		return text
	}
	return text[:77] + "..."
}

func (d Decimal) coefficientValue() *big.Int {
	if d.coefficient == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(d.coefficient)
}

func pow10(power int32) *big.Int {
	if power <= 0 {
		return big.NewInt(1)
	}
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(power)), nil)
}

func (d Decimal) String() string {
	coefficient := d.coefficientValue()
	negative := coefficient.Sign() < 0 || (coefficient.Sign() == 0 && d.negativeZero)
	coefficient.Abs(coefficient)
	digits := coefficient.String()
	var value string
	switch {
	case d.scale == 0:
		value = digits
	case len(digits) <= int(d.scale):
		value = "0." + strings.Repeat("0", int(d.scale)-len(digits)) + digits
	default:
		point := len(digits) - int(d.scale)
		value = digits[:point] + "." + digits[point:]
	}
	if negative {
		return "-" + value
	}
	return value
}

func (d Decimal) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

func (d *Decimal) UnmarshalJSON(data []byte) error {
	if d == nil {
		return errors.New("cannot unmarshal Decimal into nil receiver")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || !json.Valid(data) {
		return errInvalidJSONValue
	}
	var text string
	if data[0] == '"' {
		if err := json.Unmarshal(data, &text); err != nil {
			return fmt.Errorf("decode decimal string: %w", err)
		}
	} else {
		if data[0] != '-' && (data[0] < '0' || data[0] > '9') {
			return errInvalidJSONValue
		}
		text = string(data)
	}
	parsed, err := ParseDecimal(text)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

func (d Decimal) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

func (d *Decimal) UnmarshalText(text []byte) error {
	if d == nil {
		return errors.New("cannot unmarshal Decimal into nil receiver")
	}
	parsed, err := ParseDecimal(string(text))
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

func alignDecimals(left, right Decimal) (*big.Int, *big.Int, int32) {
	scale := left.scale
	if right.scale > scale {
		scale = right.scale
	}
	a := left.coefficientValue()
	b := right.coefficientValue()
	if delta := scale - left.scale; delta > 0 {
		a.Mul(a, pow10(delta))
	}
	if delta := scale - right.scale; delta > 0 {
		b.Mul(b, pow10(delta))
	}
	return a, b, scale
}

func (d Decimal) Add(other Decimal) Decimal {
	a, b, scale := alignDecimals(d, other)
	a.Add(a, b)
	return Decimal{coefficient: a, scale: scale}
}

func (d Decimal) Sub(other Decimal) Decimal { return d.Add(other.Neg()) }

func (d Decimal) Mul(other Decimal) Decimal {
	a := d.coefficientValue()
	a.Mul(a, other.coefficientValue())
	return Decimal{coefficient: a, scale: d.scale + other.scale}
}

func (d Decimal) Neg() Decimal {
	coefficient := d.coefficientValue()
	if coefficient.Sign() == 0 {
		return Decimal{scale: d.scale, negativeZero: !d.negativeZero}
	}
	coefficient.Neg(coefficient)
	return Decimal{coefficient: coefficient, scale: d.scale}
}

func (d Decimal) Abs() Decimal {
	coefficient := d.coefficientValue()
	coefficient.Abs(coefficient)
	return Decimal{coefficient: coefficient, scale: d.scale}
}

func (d Decimal) Cmp(other Decimal) int {
	a, b, _ := alignDecimals(d, other)
	return a.Cmp(b)
}

func (d Decimal) Equal(other Decimal) bool { return d.Cmp(other) == 0 }
func (d Decimal) Sign() int                { return d.coefficientValue().Sign() }
func (d Decimal) IsZero() bool             { return d.Sign() == 0 }
func (d Decimal) IsNegative() bool         { return d.Sign() < 0 }
func (d Decimal) IsPositive() bool         { return d.Sign() > 0 }

// Quo divides with 28 fractional decimal places and half-even rounding, then
// removes insignificant trailing zeroes. Call QuoScale when exchange-specific
// precision and rounding have been established explicitly.
func (d Decimal) Quo(other Decimal) (Decimal, error) {
	result, err := d.QuoScale(other, 28, RoundHalfEven)
	if err != nil {
		return Decimal{}, err
	}
	return result.Normalize(), nil
}

func (d Decimal) QuoScale(other Decimal, scale int32, mode RoundingMode) (Decimal, error) {
	if other.IsZero() {
		return Decimal{}, errDivisionByZero
	}
	if scale < 0 || scale > maxDecimalScale {
		return Decimal{}, fmt.Errorf("%w: scale %d", errDecimalTooLarge, scale)
	}
	if mode != RoundDown && mode != RoundHalfUp && mode != RoundHalfEven {
		return Decimal{}, fmt.Errorf("unknown rounding mode %d", mode)
	}

	numerator := d.coefficientValue()
	denominator := other.coefficientValue()
	exponent := int64(other.scale) - int64(d.scale) + int64(scale)
	if exponent >= 0 {
		numerator.Mul(numerator, pow10(int32(exponent)))
	} else {
		denominator.Mul(denominator, pow10(int32(-exponent)))
	}
	if denominator.Sign() < 0 {
		denominator.Neg(denominator)
		numerator.Neg(numerator)
	}

	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	if remainder.Sign() != 0 && mode != RoundDown {
		twiceRemainder := new(big.Int).Lsh(new(big.Int).Abs(remainder), 1)
		comparison := twiceRemainder.Cmp(denominator)
		roundAway := comparison > 0 || (comparison == 0 && mode == RoundHalfUp)
		if comparison == 0 && mode == RoundHalfEven {
			roundAway = new(big.Int).Abs(quotient).Bit(0) == 1
		}
		if roundAway {
			if numerator.Sign() < 0 {
				quotient.Sub(quotient, big.NewInt(1))
			} else {
				quotient.Add(quotient, big.NewInt(1))
			}
		}
	}
	negativeZero := quotient.Sign() == 0 && numerator.Sign() < 0
	return Decimal{coefficient: quotient, scale: scale, negativeZero: negativeZero}, nil
}

func (d Decimal) Normalize() Decimal {
	coefficient := d.coefficientValue()
	scale := d.scale
	if coefficient.Sign() == 0 {
		return Zero()
	}
	ten := big.NewInt(10)
	for scale > 0 {
		quotient, remainder := new(big.Int), new(big.Int)
		quotient.QuoRem(coefficient, ten, remainder)
		if remainder.Sign() != 0 {
			break
		}
		coefficient = quotient
		scale--
	}
	return Decimal{coefficient: coefficient, scale: scale}
}

func (d Decimal) Float64() (float64, error) {
	denominator := pow10(d.scale)
	value, _ := new(big.Rat).SetFrac(d.coefficientValue(), denominator).Float64()
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, errors.New("decimal is outside finite float64 range")
	}
	return value, nil
}
