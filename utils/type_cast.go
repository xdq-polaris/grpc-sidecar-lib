package utils

import "unsafe"

func UnsafeCast[T any, U any](input T) U {
	return *(*U)(unsafe.Pointer(&input))
}
