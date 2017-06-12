package prometheus

import (
	"math"
	"sync/atomic"
	"time"
	"unsafe"
)

type atomicUint64 uint64

func (a *atomicUint64) store(v uint64) {
	atomic.StoreUint64(a.ptr(), v)
}

func (a *atomicUint64) load() uint64 {
	return atomic.LoadUint64(a.ptr())
}

func (a *atomicUint64) add(v uint64) {
	atomic.AddUint64(a.ptr(), v)
}

func (a *atomicUint64) ptr() *uint64 {
	return (*uint64)(unsafe.Pointer(a))
}

type atomicFloat64 float64

func (a *atomicFloat64) store(v float64) {
	atomic.StoreUint64(a.ptr(), math.Float64bits(v))
}

func (a *atomicFloat64) load() float64 {
	return math.Float64frombits(atomic.LoadUint64(a.ptr()))
}

func (a *atomicFloat64) add(v float64) {
	for {
		ptr := a.ptr()
		old := atomic.LoadUint64(ptr)
		new := math.Float64bits(math.Float64frombits(old) + v)
		if atomic.CompareAndSwapUint64(ptr, old, new) {
			break
		}
	}
}

func (a *atomicFloat64) ptr() *uint64 {
	return (*uint64)(unsafe.Pointer(a))
}

type atomicTime int64

func makeAtomicTime(v time.Time) atomicTime {
	return atomicTime(timeToUnix(v))
}

func (a *atomicTime) store(v time.Time) {
	atomic.StoreInt64(a.ptr(), timeToUnix(v))
}

func (a *atomicTime) load() time.Time {
	return unixToTime(atomic.LoadInt64(a.ptr()))
}

func (a *atomicTime) ptr() *int64 {
	return (*int64)(unsafe.Pointer(a))
}

func timeToUnix(t time.Time) int64 {
	t = t.In(time.UTC)
	return (t.Unix() * 1e3) + int64(t.Nanosecond()/1e6)
}

func unixToTime(t int64) time.Time {
	return time.Unix(t/1e3, (t%1e3)*1e6).In(time.UTC)
}
