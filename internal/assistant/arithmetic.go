package assistant

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math/big"
	"strings"
	"time"
	"unicode"
)

var (
	errArithmeticZero        = errors.New("division by zero")
	errArithmeticBound       = errors.New("calculation too large")
	errArithmeticUnsupported = errors.New("unsupported calculation")
)

func arithmeticExpression(input string) (string, bool) {
	q := strings.TrimSpace(strings.ToLower(input))
	explicit := false
	for _, prefix := range []string{"what is ", "what's ", "calculate ", "compute ", "evaluate "} {
		if strings.HasPrefix(q, prefix) {
			q, explicit = strings.TrimSpace(q[len(prefix):]), true
			break
		}
	}
	q = strings.TrimSpace(strings.TrimRight(q, "?="))
	if !explicit {
		if _, err := time.Parse("2006-01-02", q); err == nil {
			return "", false // a bare calendar date is not subtraction
		}
	}
	q = strings.NewReplacer("×", "*", "÷", "/", "−", "-").Replace(q)
	digit := false
	for _, r := range q {
		if r >= '0' && r <= '9' {
			digit = true
			continue
		}
		if !unicode.IsSpace(r) && !strings.ContainsRune(".+-*/%^()e", r) {
			return "", false
		}
	}
	return q, digit && strings.ContainsAny(strings.TrimLeft(q, "+- \t\n"), "+-*/%^")
}

// IsCalculation recognizes read-only numeric requests before the note fallback.
// Unsupported operators can still go to the selected model; explicit note
// commands are handled by the caller before this classifier.
func IsCalculation(input string) bool { _, ok := arithmeticExpression(input); return ok }

// Calculate evaluates only bounded numeric syntax. It has no variables,
// function calls, storage, filesystem or model access. Rational arithmetic keeps
// ordinary decimal calculations exact (0.1 + 0.2 is 0.3).
func Calculate(input string) (string, bool) {
	q, ok := arithmeticExpression(input)
	if !ok {
		return "", false
	}
	if len(q) > 2048 {
		return "That calculation is too large.", true
	}
	if strings.ContainsAny(q, "^e") || strings.Contains(q, "**") {
		return "", false // the selected answer model handles these forms
	}
	expression, err := parser.ParseExpr(q)
	if err != nil {
		return "That calculation is incomplete or invalid.", true
	}
	nodes := 0
	value, err := arithmeticValue(expression, 0, &nodes)
	if errors.Is(err, errArithmeticUnsupported) {
		return "", false
	}
	if errors.Is(err, errArithmeticZero) {
		return "Cannot divide by zero.", true
	}
	if err != nil {
		return "That calculation is too large or invalid.", true
	}
	if value.IsInt() {
		return value.Num().String(), true
	}
	// Print terminating decimals exactly; retain an exact fraction otherwise.
	denominator := new(big.Int).Set(value.Denom())
	places := 0
	for _, factor := range []int64{2, 5} {
		count := 0
		f, remainder := big.NewInt(factor), new(big.Int)
		for {
			remainder.Mod(denominator, f)
			if remainder.Sign() != 0 {
				break
			}
			denominator.Quo(denominator, f)
			count++
		}
		places = max(places, count)
	}
	if denominator.Cmp(big.NewInt(1)) == 0 && places <= 512 {
		return value.FloatString(places), true
	}
	return value.RatString(), true
}

func arithmeticValue(expression ast.Expr, depth int, nodes *int) (*big.Rat, error) {
	*nodes++
	if depth > 32 || *nodes > 128 {
		return nil, errArithmeticBound
	}
	var result *big.Rat
	switch e := expression.(type) {
	case *ast.BasicLit:
		if e.Kind != token.INT && e.Kind != token.FLOAT || len(e.Value) > 512 {
			return nil, errArithmeticUnsupported
		}
		var ok bool
		result, ok = new(big.Rat).SetString(e.Value)
		if !ok {
			return nil, errArithmeticUnsupported
		}
	case *ast.ParenExpr:
		return arithmeticValue(e.X, depth+1, nodes)
	case *ast.UnaryExpr:
		v, err := arithmeticValue(e.X, depth+1, nodes)
		if err != nil {
			return nil, err
		}
		switch e.Op {
		case token.ADD:
			result = v
		case token.SUB:
			result = v.Neg(v)
		default:
			return nil, errArithmeticUnsupported
		}
	case *ast.BinaryExpr:
		a, err := arithmeticValue(e.X, depth+1, nodes)
		if err != nil {
			return nil, err
		}
		b, err := arithmeticValue(e.Y, depth+1, nodes)
		if err != nil {
			return nil, err
		}
		result = new(big.Rat)
		switch e.Op {
		case token.ADD:
			result.Add(a, b)
		case token.SUB:
			result.Sub(a, b)
		case token.MUL:
			result.Mul(a, b)
		case token.QUO:
			if b.Sign() == 0 {
				return nil, errArithmeticZero
			}
			result.Quo(a, b)
		case token.REM:
			if b.Sign() == 0 {
				return nil, errArithmeticZero
			}
			if !a.IsInt() || !b.IsInt() {
				return nil, errArithmeticUnsupported
			}
			result.SetInt(new(big.Int).Rem(a.Num(), b.Num()))
		default:
			return nil, errArithmeticUnsupported
		}
	default:
		return nil, errArithmeticUnsupported
	}
	if result.Num().BitLen() > 8192 || result.Denom().BitLen() > 8192 {
		return nil, errArithmeticBound
	}
	return result, nil
}
