package stdlib

import (
	"errors"

	"go.starlark.net/starlark"
	"gonum.org/v1/gonum/mat"
)

// MaxMatrixDim bounds every matrix axis so O(n^3) factorizations stay small;
// Starlark step limits do not count work inside gonum.
const MaxMatrixDim = 256

func matrixOf(t *starlark.Thread, v starlark.Value) (*mat.Dense, error) {
	rows, err := sequence(v, MaxMatrixDim)
	if err != nil {
		return nil, errors.New("matrix must be a list of at most 256 rows")
	}
	if rows.Len() == 0 {
		return nil, errors.New("matrix must not be empty")
	}
	var data []float64
	cols := -1
	for i := 0; i < rows.Len(); i++ {
		row, err := floatsOf(t, rows.Index(i), 1, MaxMatrixDim)
		if err != nil {
			return nil, err
		}
		if cols >= 0 && len(row) != cols {
			return nil, errors.New("matrix rows must have the same length")
		}
		cols = len(row)
		data = append(data, row...)
	}
	return mat.NewDense(rows.Len(), cols, data), nil
}

// operand parses a matrix, or a flat vector as a single column.
func operand(t *starlark.Thread, v starlark.Value) (*mat.Dense, bool, error) {
	if seq, ok := v.(starlark.Indexable); ok && seq.Len() > 0 {
		switch seq.Index(0).(type) {
		case starlark.Int, starlark.Float:
			xs, err := floatsOf(t, v, 1, MaxMatrixDim)
			if err != nil {
				return nil, false, err
			}
			return mat.NewDense(len(xs), 1, xs), true, nil
		}
	}
	m, err := matrixOf(t, v)
	return m, false, err
}

func matrixValue(m mat.Matrix, vector bool) (starlark.Value, error) {
	r, c := m.Dims()
	rows := make([]starlark.Value, r)
	for i := range rows {
		row := make([]starlark.Value, c)
		for j := range row {
			f, err := floatResult(m.At(i, j))
			if err != nil {
				return nil, err
			}
			row[j] = f
		}
		rows[i] = starlark.NewList(row)
		if vector {
			rows[i] = row[0]
		}
	}
	return starlark.NewList(rows), nil
}

func linalgCall(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var av, bv starlark.Value
	var err error
	switch b.Name() {
	case "linalg.inv", "linalg.det":
		err = unpack(b, args, kwargs, "a", &av)
	default:
		err = unpack(b, args, kwargs, "a", &av, "b", &bv)
	}
	if err != nil {
		return nil, err
	}
	a, err := matrixOf(t, av)
	if err != nil {
		return nil, err
	}
	ar, ac := a.Dims()
	square := ar == ac
	switch b.Name() {
	case "linalg.det":
		if !square {
			return nil, errors.New("matrix must be square")
		}
		return floatResult(mat.Det(a))
	case "linalg.inv":
		if !square {
			return nil, errors.New("matrix must be square")
		}
		var inv mat.Dense
		if inv.Inverse(a) != nil {
			return nil, errors.New("matrix is singular or ill-conditioned")
		}
		return matrixValue(&inv, false)
	case "linalg.matmul":
		m, err := matrixOf(t, bv)
		if err != nil {
			return nil, err
		}
		if mr, _ := m.Dims(); mr != ac {
			return nil, errors.New("matrix shapes are not aligned")
		}
		var out mat.Dense
		out.Mul(a, m)
		return matrixValue(&out, false)
	}
	if b.Name() == "linalg.solve" && !square {
		return nil, errors.New("matrix must be square; use linalg.lstsq")
	}
	rhs, vector, err := operand(t, bv)
	if err != nil {
		return nil, err
	}
	if br, _ := rhs.Dims(); br != ar {
		return nil, errors.New("matrix shapes are not aligned")
	}
	var x mat.Dense
	if x.Solve(a, rhs) != nil {
		return nil, errors.New("matrix is singular, rank-deficient or ill-conditioned")
	}
	return matrixValue(&x, vector)
}
