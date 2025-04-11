package diag

import "log/slog"

func ErrAttr(err error) slog.Attr {
	return slog.Any("err", err)
}
