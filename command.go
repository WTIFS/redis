package redis

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis/v7/internal"
	"github.com/go-redis/redis/v7/internal/proto"
	"github.com/go-redis/redis/v7/internal/util"
)

type Cmder interface {
	Name() string
	Args() []interface{}
	stringArg(int) string

	readTimeout() *time.Duration
	readReply(rd *proto.Reader) error

	setErr(error)
	Err() error
}

func setCmdsErr(cmds []Cmder, e error) {
	for _, cmd := range cmds {
		if cmd.Err() == nil {
			cmd.setErr(e)
		}
	}
}

func cmdsFirstErr(cmds []Cmder) error {
	for _, cmd := range cmds {
		if err := cmd.Err(); err != nil {
			return err
		}
	}
	return nil
}

func writeCmd(wr *proto.Writer, cmds ...Cmder) error {
	for _, cmd := range cmds {
		err := wr.WriteArgs(cmd.Args())
		if err != nil {
			return err
		}
	}
	return nil
}

func cmdString(cmd Cmder, val interface{}) string {
	ss := make([]string, 0, len(cmd.Args()))
	for _, arg := range cmd.Args() {
		ss = append(ss, fmt.Sprint(arg))
	}
	s := strings.Join(ss, " ")
	if err := cmd.Err(); err != nil {
		return s + ": " + err.Error()
	}
	if val != nil {
		switch vv := val.(type) {
		case []byte:
			return s + ": " + string(vv)
		default:
			return s + ": " + fmt.Sprint(val)
		}
	}
	return s
}

func cmdFirstKeyPos(cmd Cmder, info *CommandInfo) int {
	switch cmd.Name() {
	case "eval", "evalsha":
		if cmd.stringArg(2) != "0" {
			return 3
		}

		return 0
	case "publish":
		return 1
	}
	if info == nil {
		return 0
	}
	return int(info.FirstKeyPos)
}

//------------------------------------------------------------------------------

type baseCmd struct {
	args []interface{}
	err  error

	_readTimeout *time.Duration
}

var _ Cmder = (*Cmd)(nil)

func (cmd *baseCmd) Name() string {
	if len(cmd.args) == 0 {
		return ""
	}
	// Cmd name must be lower cased.
	return internal.ToLower(cmd.stringArg(0))
}

func (cmd *baseCmd) Args() []interface{} {
	return cmd.args
}

func (cmd *baseCmd) stringArg(pos int) string {
	if pos < 0 || pos >= len(cmd.args) {
		return ""
	}
	s, _ := cmd.args[pos].(string)
	return s
}

func (cmd *baseCmd) setErr(e error) {
	cmd.err = e
}

func (cmd *baseCmd) Err() error {
	return cmd.err
}

func (cmd *baseCmd) readTimeout() *time.Duration {
	return cmd._readTimeout
}

func (cmd *baseCmd) setReadTimeout(d time.Duration) {
	cmd._readTimeout = &d
}

//------------------------------------------------------------------------------

type Cmd struct {
	baseCmd

	val interface{}
}

func NewCmd(args ...interface{}) *Cmd {
	return &Cmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *Cmd) Val() interface{} {
	return cmd.val
}

func (cmd *Cmd) Result() (interface{}, error) {
	return cmd.val, cmd.err
}

func (cmd *Cmd) String() (string, error) {
	if cmd.err != nil {
		return "", cmd.err
	}
	switch val := cmd.val.(type) {
	case string:
		return val, nil
	default:
		err := fmt.Errorf("redis: unexpected type=%T for String", val)
		return "", err
	}
}

func (cmd *Cmd) Int() (int, error) {
	if cmd.err != nil {
		return 0, cmd.err
	}
	switch val := cmd.val.(type) {
	case int64:
		return int(val), nil
	case string:
		return strconv.Atoi(val)
	default:
		err := fmt.Errorf("redis: unexpected type=%T for Int", val)
		return 0, err
	}
}

func (cmd *Cmd) Int64() (int64, error) {
	if cmd.err != nil {
		return 0, cmd.err
	}
	switch val := cmd.val.(type) {
	case int64:
		return val, nil
	case string:
		return strconv.ParseInt(val, 10, 64)
	default:
		err := fmt.Errorf("redis: unexpected type=%T for Int64", val)
		return 0, err
	}
}

func (cmd *Cmd) Uint64() (uint64, error) {
	if cmd.err != nil {
		return 0, cmd.err
	}
	switch val := cmd.val.(type) {
	case int64:
		return uint64(val), nil
	case string:
		return strconv.ParseUint(val, 10, 64)
	default:
		err := fmt.Errorf("redis: unexpected type=%T for Uint64", val)
		return 0, err
	}
}

func (cmd *Cmd) Float32() (float32, error) {
	if cmd.err != nil {
		return 0, cmd.err
	}
	switch val := cmd.val.(type) {
	case int64:
		return float32(val), nil
	case string:
		f, err := strconv.ParseFloat(val, 32)
		if err != nil {
			return 0, err
		}
		return float32(f), nil
	default:
		err := fmt.Errorf("redis: unexpected type=%T for Float32", val)
		return 0, err
	}
}

func (cmd *Cmd) Float64() (float64, error) {
	if cmd.err != nil {
		return 0, cmd.err
	}
	switch val := cmd.val.(type) {
	case int64:
		return float64(val), nil
	case string:
		return strconv.ParseFloat(val, 64)
	default:
		err := fmt.Errorf("redis: unexpected type=%T for Float64", val)
		return 0, err
	}
}

func (cmd *Cmd) Bool() (bool, error) {
	if cmd.err != nil {
		return false, cmd.err
	}
	switch val := cmd.val.(type) {
	case int64:
		return val != 0, nil
	case string:
		return strconv.ParseBool(val)
	default:
		err := fmt.Errorf("redis: unexpected type=%T for Bool", val)
		return false, err
	}
}

func (cmd *Cmd) readReply(rd *proto.Reader) error {
	cmd.val, cmd.err = rd.ReadReply(sliceParser)
	return cmd.err
}

// Implements proto.MultiBulkParse
func sliceParser(rd *proto.Reader, n int64) (interface{}, error) {
	vals := make([]interface{}, n)
	for i := 0; i < len(vals); i++ {
		v, err := rd.ReadReply(sliceParser)
		if err != nil {
			if err == Nil {
				vals[i] = nil
				continue
			}
			if err, ok := err.(proto.RedisError); ok {
				vals[i] = err
				continue
			}
			return nil, err
		}
		vals[i] = v
	}
	return vals, nil
}

//------------------------------------------------------------------------------

type SliceCmd struct {
	baseCmd

	val []interface{}
}

var _ Cmder = (*SliceCmd)(nil)

func NewSliceCmd(args ...interface{}) *SliceCmd {
	return &SliceCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *SliceCmd) Val() []interface{} {
	return cmd.val
}

func (cmd *SliceCmd) Result() ([]interface{}, error) {
	return cmd.val, cmd.err
}

func (cmd *SliceCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *SliceCmd) readReply(rd *proto.Reader) error {
	var v interface{}
	v, cmd.err = rd.ReadArrayReply(sliceParser)
	if cmd.err != nil {
		return cmd.err
	}
	cmd.val = v.([]interface{})
	return nil
}

//------------------------------------------------------------------------------

type StatusCmd struct {
	baseCmd

	val string
}

var _ Cmder = (*StatusCmd)(nil)

func NewStatusCmd(args ...interface{}) *StatusCmd {
	return &StatusCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *StatusCmd) Val() string {
	return cmd.val
}

func (cmd *StatusCmd) Result() (string, error) {
	return cmd.val, cmd.err
}

func (cmd *StatusCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *StatusCmd) readReply(rd *proto.Reader) error {
	cmd.val, cmd.err = rd.ReadString()
	return cmd.err
}

//------------------------------------------------------------------------------

type IntCmd struct {
	baseCmd

	val int64
}

var _ Cmder = (*IntCmd)(nil)

func NewIntCmd(args ...interface{}) *IntCmd {
	return &IntCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *IntCmd) Val() int64 {
	return cmd.val
}

func (cmd *IntCmd) Result() (int64, error) {
	return cmd.val, cmd.err
}

func (cmd *IntCmd) Uint64() (uint64, error) {
	return uint64(cmd.val), cmd.err
}

func (cmd *IntCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *IntCmd) readReply(rd *proto.Reader) error {
	cmd.val, cmd.err = rd.ReadIntReply()
	return cmd.err
}

//------------------------------------------------------------------------------

type IntSliceCmd struct {
	baseCmd

	val []int64
}

var _ Cmder = (*IntSliceCmd)(nil)

func NewIntSliceCmd(args ...interface{}) *IntSliceCmd {
	return &IntSliceCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *IntSliceCmd) Val() []int64 {
	return cmd.val
}

func (cmd *IntSliceCmd) Result() ([]int64, error) {
	return cmd.val, cmd.err
}

func (cmd *IntSliceCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *IntSliceCmd) readReply(rd *proto.Reader) error {
	_, cmd.err = rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
		cmd.val = make([]int64, n)
		for i := 0; i < len(cmd.val); i++ {
			num, err := rd.ReadIntReply()
			if err != nil {
				return nil, err
			}
			cmd.val[i] = num
		}
		return nil, nil
	})
	return cmd.err
}

//------------------------------------------------------------------------------

type DurationCmd struct {
	baseCmd

	val       time.Duration
	precision time.Duration
}

var _ Cmder = (*DurationCmd)(nil)

func NewDurationCmd(precision time.Duration, args ...interface{}) *DurationCmd {
	return &DurationCmd{
		baseCmd:   baseCmd{args: args},
		precision: precision,
	}
}

func (cmd *DurationCmd) Val() time.Duration {
	return cmd.val
}

func (cmd *DurationCmd) Result() (time.Duration, error) {
	return cmd.val, cmd.err
}

func (cmd *DurationCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *DurationCmd) readReply(rd *proto.Reader) error {
	var n int64
	n, cmd.err = rd.ReadIntReply()
	if cmd.err != nil {
		return cmd.err
	}
	switch n {
	// -2 if the key does not exist
	// -1 if the key exists but has no associated expire
	case -2, -1:
		cmd.val = time.Duration(n)
	default:
		cmd.val = time.Duration(n) * cmd.precision
	}
	return nil
}

//------------------------------------------------------------------------------

type TimeCmd struct {
	baseCmd

	val time.Time
}

var _ Cmder = (*TimeCmd)(nil)

func NewTimeCmd(args ...interface{}) *TimeCmd {
	return &TimeCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *TimeCmd) Val() time.Time {
	return cmd.val
}

func (cmd *TimeCmd) Result() (time.Time, error) {
	return cmd.val, cmd.err
}

func (cmd *TimeCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *TimeCmd) readReply(rd *proto.Reader) error {
	_, cmd.err = rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
		if n != 2 {
			return nil, fmt.Errorf("got %d elements, expected 2", n)
		}

		sec, err := rd.ReadInt()
		if err != nil {
			return nil, err
		}

		microsec, err := rd.ReadInt()
		if err != nil {
			return nil, err
		}

		cmd.val = time.Unix(sec, microsec*1000)
		return nil, nil
	})
	return cmd.err
}

//------------------------------------------------------------------------------

type BoolCmd struct {
	baseCmd

	val bool
}

var _ Cmder = (*BoolCmd)(nil)

func NewBoolCmd(args ...interface{}) *BoolCmd {
	return &BoolCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *BoolCmd) Val() bool {
	return cmd.val
}

func (cmd *BoolCmd) Result() (bool, error) {
	return cmd.val, cmd.err
}

func (cmd *BoolCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *BoolCmd) readReply(rd *proto.Reader) error {
	var v interface{}
	v, cmd.err = rd.ReadReply(nil)
	// `SET key value NX` returns nil when key already exists. But
	// `SETNX key value` returns bool (0/1). So convert nil to bool.
	if cmd.err == Nil {
		cmd.val = false
		cmd.err = nil
		return nil
	}
	if cmd.err != nil {
		return cmd.err
	}
	switch v := v.(type) {
	case int64:
		cmd.val = v == 1
		return nil
	case string:
		cmd.val = v == "OK"
		return nil
	default:
		cmd.err = fmt.Errorf("got %T, wanted int64 or string", v)
		return cmd.err
	}
}

//------------------------------------------------------------------------------

type StringCmd struct {
	baseCmd

	val string
}

var _ Cmder = (*StringCmd)(nil)

func NewStringCmd(args ...interface{}) *StringCmd {
	return &StringCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *StringCmd) Val() string {
	return cmd.val
}

func (cmd *StringCmd) Result() (string, error) {
	return cmd.Val(), cmd.err
}

func (cmd *StringCmd) Bytes() ([]byte, error) {
	return util.StringToBytes(cmd.val), cmd.err
}

func (cmd *StringCmd) Int() (int, error) {
	if cmd.err != nil {
		return 0, cmd.err
	}
	return strconv.Atoi(cmd.Val())
}

func (cmd *StringCmd) Int64() (int64, error) {
	if cmd.err != nil {
		return 0, cmd.err
	}
	return strconv.ParseInt(cmd.Val(), 10, 64)
}

func (cmd *StringCmd) Uint64() (uint64, error) {
	if cmd.err != nil {
		return 0, cmd.err
	}
	return strconv.ParseUint(cmd.Val(), 10, 64)
}

func (cmd *StringCmd) Float32() (float32, error) {
	if cmd.err != nil {
		return 0, cmd.err
	}
	f, err := strconv.ParseFloat(cmd.Val(), 32)
	if err != nil {
		return 0, err
	}
	return float32(f), nil
}

func (cmd *StringCmd) Float64() (float64, error) {
	if cmd.err != nil {
		return 0, cmd.err
	}
	return strconv.ParseFloat(cmd.Val(), 64)
}

func (cmd *StringCmd) Time() (time.Time, error) {
	if cmd.err != nil {
		return time.Time{}, cmd.err
	}
	return time.Parse(time.RFC3339, cmd.Val())
}

func (cmd *StringCmd) Scan(val interface{}) error {
	if cmd.err != nil {
		return cmd.err
	}
	return proto.Scan([]byte(cmd.val), val)
}

func (cmd *StringCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *StringCmd) readReply(rd *proto.Reader) error {
	cmd.val, cmd.err = rd.ReadString()
	return cmd.err
}

//------------------------------------------------------------------------------

type FloatCmd struct {
	baseCmd

	val float64
}

var _ Cmder = (*FloatCmd)(nil)

func NewFloatCmd(args ...interface{}) *FloatCmd {
	return &FloatCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *FloatCmd) Val() float64 {
	return cmd.val
}

func (cmd *FloatCmd) Result() (float64, error) {
	return cmd.Val(), cmd.Err()
}

func (cmd *FloatCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *FloatCmd) readReply(rd *proto.Reader) error {
	cmd.val, cmd.err = rd.ReadFloatReply()
	return cmd.err
}

//------------------------------------------------------------------------------

type StringSliceCmd struct {
	baseCmd

	val []string
}

var _ Cmder = (*StringSliceCmd)(nil)

func NewStringSliceCmd(args ...interface{}) *StringSliceCmd {
	return &StringSliceCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *StringSliceCmd) Val() []string {
	return cmd.val
}

func (cmd *StringSliceCmd) Result() ([]string, error) {
	return cmd.Val(), cmd.Err()
}

func (cmd *StringSliceCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *StringSliceCmd) ScanSlice(container interface{}) error {
	return proto.ScanSlice(cmd.Val(), container)
}

func (cmd *StringSliceCmd) readReply(rd *proto.Reader) error {
	_, cmd.err = rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
		cmd.val = make([]string, n)
		for i := 0; i < len(cmd.val); i++ {
			switch s, err := rd.ReadString(); {
			case err == Nil:
				cmd.val[i] = ""
			case err != nil:
				return nil, err
			default:
				cmd.val[i] = s
			}
		}
		return nil, nil
	})
	return cmd.err
}

//------------------------------------------------------------------------------

type BoolSliceCmd struct {
	baseCmd

	val []bool
}

var _ Cmder = (*BoolSliceCmd)(nil)

func NewBoolSliceCmd(args ...interface{}) *BoolSliceCmd {
	return &BoolSliceCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *BoolSliceCmd) Val() []bool {
	return cmd.val
}

func (cmd *BoolSliceCmd) Result() ([]bool, error) {
	return cmd.val, cmd.err
}

func (cmd *BoolSliceCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *BoolSliceCmd) readReply(rd *proto.Reader) error {
	_, cmd.err = rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
		cmd.val = make([]bool, n)
		for i := 0; i < len(cmd.val); i++ {
			n, err := rd.ReadIntReply()
			if err != nil {
				return nil, err
			}
			cmd.val[i] = n == 1
		}
		return nil, nil
	})
	return cmd.err
}

//------------------------------------------------------------------------------

type StringStringMapCmd struct {
	baseCmd

	val map[string]string
}

var _ Cmder = (*StringStringMapCmd)(nil)

func NewStringStringMapCmd(args ...interface{}) *StringStringMapCmd {
	return &StringStringMapCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *StringStringMapCmd) Val() map[string]string {
	return cmd.val
}

func (cmd *StringStringMapCmd) Result() (map[string]string, error) {
	return cmd.val, cmd.err
}

func (cmd *StringStringMapCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *StringStringMapCmd) readReply(rd *proto.Reader) error {
	_, cmd.err = rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
		cmd.val = make(map[string]string, n/2)
		for i := int64(0); i < n; i += 2 {
			key, err := rd.ReadString()
			if err != nil {
				return nil, err
			}

			value, err := rd.ReadString()
			if err != nil {
				return nil, err
			}

			cmd.val[key] = value
		}
		return nil, nil
	})
	return cmd.err
}

//------------------------------------------------------------------------------

type StringIntMapCmd struct {
	baseCmd

	val map[string]int64
}

var _ Cmder = (*StringIntMapCmd)(nil)

func NewStringIntMapCmd(args ...interface{}) *StringIntMapCmd {
	return &StringIntMapCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *StringIntMapCmd) Val() map[string]int64 {
	return cmd.val
}

func (cmd *StringIntMapCmd) Result() (map[string]int64, error) {
	return cmd.val, cmd.err
}

func (cmd *StringIntMapCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *StringIntMapCmd) readReply(rd *proto.Reader) error {
	_, cmd.err = rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
		cmd.val = make(map[string]int64, n/2)
		for i := int64(0); i < n; i += 2 {
			key, err := rd.ReadString()
			if err != nil {
				return nil, err
			}

			n, err := rd.ReadIntReply()
			if err != nil {
				return nil, err
			}

			cmd.val[key] = n
		}
		return nil, nil
	})
	return cmd.err
}

//------------------------------------------------------------------------------

type StringStructMapCmd struct {
	baseCmd

	val map[string]struct{}
}

var _ Cmder = (*StringStructMapCmd)(nil)

func NewStringStructMapCmd(args ...interface{}) *StringStructMapCmd {
	return &StringStructMapCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *StringStructMapCmd) Val() map[string]struct{} {
	return cmd.val
}

func (cmd *StringStructMapCmd) Result() (map[string]struct{}, error) {
	return cmd.val, cmd.err
}

func (cmd *StringStructMapCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *StringStructMapCmd) readReply(rd *proto.Reader) error {
	_, cmd.err = rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
		cmd.val = make(map[string]struct{}, n)
		for i := int64(0); i < n; i++ {
			key, err := rd.ReadString()
			if err != nil {
				return nil, err
			}
			cmd.val[key] = struct{}{}
		}
		return nil, nil
	})
	return cmd.err
}

//------------------------------------------------------------------------------

type XMessage struct {
	ID     string
	Values map[string]interface{}
}

type XMessageSliceCmd struct {
	baseCmd

	val []XMessage
}

var _ Cmder = (*XMessageSliceCmd)(nil)

func NewXMessageSliceCmd(args ...interface{}) *XMessageSliceCmd {
	return &XMessageSliceCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *XMessageSliceCmd) Val() []XMessage {
	return cmd.val
}

func (cmd *XMessageSliceCmd) Result() ([]XMessage, error) {
	return cmd.val, cmd.err
}

func (cmd *XMessageSliceCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *XMessageSliceCmd) readReply(rd *proto.Reader) error {
	var v interface{}
	v, cmd.err = rd.ReadArrayReply(xMessageSliceParser)
	if cmd.err != nil {
		return cmd.err
	}
	cmd.val = v.([]XMessage)
	return nil
}

// Implements proto.MultiBulkParse
func xMessageSliceParser(rd *proto.Reader, n int64) (interface{}, error) {
	msgs := make([]XMessage, n)
	for i := 0; i < len(msgs); i++ {
		i := i
		_, err := rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
			id, err := rd.ReadString()
			if err != nil {
				return nil, err
			}

			v, err := rd.ReadArrayReply(stringInterfaceMapParser)
			if err != nil {
				return nil, err
			}

			msgs[i] = XMessage{
				ID:     id,
				Values: v.(map[string]interface{}),
			}
			return nil, nil
		})
		if err != nil {
			return nil, err
		}
	}
	return msgs, nil
}

// Implements proto.MultiBulkParse
func stringInterfaceMapParser(rd *proto.Reader, n int64) (interface{}, error) {
	m := make(map[string]interface{}, n/2)
	for i := int64(0); i < n; i += 2 {
		key, err := rd.ReadString()
		if err != nil {
			return nil, err
		}

		value, err := rd.ReadString()
		if err != nil {
			return nil, err
		}

		m[key] = value
	}
	return m, nil
}

//------------------------------------------------------------------------------

type XStream struct {
	Stream   string
	Messages []XMessage
}

type XStreamSliceCmd struct {
	baseCmd

	val []XStream
}

var _ Cmder = (*XStreamSliceCmd)(nil)

func NewXStreamSliceCmd(args ...interface{}) *XStreamSliceCmd {
	return &XStreamSliceCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *XStreamSliceCmd) Val() []XStream {
	return cmd.val
}

func (cmd *XStreamSliceCmd) Result() ([]XStream, error) {
	return cmd.val, cmd.err
}

func (cmd *XStreamSliceCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *XStreamSliceCmd) readReply(rd *proto.Reader) error {
	_, cmd.err = rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
		cmd.val = make([]XStream, n)
		for i := 0; i < len(cmd.val); i++ {
			i := i
			_, err := rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
				if n != 2 {
					return nil, fmt.Errorf("got %d, wanted 2", n)
				}

				stream, err := rd.ReadString()
				if err != nil {
					return nil, err
				}

				v, err := rd.ReadArrayReply(xMessageSliceParser)
				if err != nil {
					return nil, err
				}

				cmd.val[i] = XStream{
					Stream:   stream,
					Messages: v.([]XMessage),
				}
				return nil, nil
			})
			if err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	return cmd.err
}

//------------------------------------------------------------------------------

type XPending struct {
	Count     int64
	Lower     string
	Higher    string
	Consumers map[string]int64
}

type XPendingCmd struct {
	baseCmd
	val *XPending
}

var _ Cmder = (*XPendingCmd)(nil)

func NewXPendingCmd(args ...interface{}) *XPendingCmd {
	return &XPendingCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *XPendingCmd) Val() *XPending {
	return cmd.val
}

func (cmd *XPendingCmd) Result() (*XPending, error) {
	return cmd.val, cmd.err
}

func (cmd *XPendingCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *XPendingCmd) readReply(rd *proto.Reader) error {
	_, cmd.err = rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
		if n != 4 {
			return nil, fmt.Errorf("got %d, wanted 4", n)
		}

		count, err := rd.ReadIntReply()
		if err != nil {
			return nil, err
		}

		lower, err := rd.ReadString()
		if err != nil && err != Nil {
			return nil, err
		}

		higher, err := rd.ReadString()
		if err != nil && err != Nil {
			return nil, err
		}

		cmd.val = &XPending{
			Count:  count,
			Lower:  lower,
			Higher: higher,
		}
		_, err = rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
			for i := int64(0); i < n; i++ {
				_, err = rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
					if n != 2 {
						return nil, fmt.Errorf("got %d, wanted 2", n)
					}

					consumerName, err := rd.ReadString()
					if err != nil {
						return nil, err
					}

					consumerPending, err := rd.ReadInt()
					if err != nil {
						return nil, err
					}

					if cmd.val.Consumers == nil {
						cmd.val.Consumers = make(map[string]int64)
					}
					cmd.val.Consumers[consumerName] = consumerPending

					return nil, nil
				})
				if err != nil {
					return nil, err
				}
			}
			return nil, nil
		})
		if err != nil && err != Nil {
			return nil, err
		}

		return nil, nil
	})
	return cmd.err
}

//------------------------------------------------------------------------------

type XPendingExt struct {
	ID         string
	Consumer   string
	Idle       time.Duration
	RetryCount int64
}

type XPendingExtCmd struct {
	baseCmd
	val []XPendingExt
}

var _ Cmder = (*XPendingExtCmd)(nil)

func NewXPendingExtCmd(args ...interface{}) *XPendingExtCmd {
	return &XPendingExtCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *XPendingExtCmd) Val() []XPendingExt {
	return cmd.val
}

func (cmd *XPendingExtCmd) Result() ([]XPendingExt, error) {
	return cmd.val, cmd.err
}

func (cmd *XPendingExtCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *XPendingExtCmd) readReply(rd *proto.Reader) error {
	_, cmd.err = rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
		cmd.val = make([]XPendingExt, 0, n)
		for i := int64(0); i < n; i++ {
			_, err := rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
				if n != 4 {
					return nil, fmt.Errorf("got %d, wanted 4", n)
				}

				id, err := rd.ReadString()
				if err != nil {
					return nil, err
				}

				consumer, err := rd.ReadString()
				if err != nil && err != Nil {
					return nil, err
				}

				idle, err := rd.ReadIntReply()
				if err != nil && err != Nil {
					return nil, err
				}

				retryCount, err := rd.ReadIntReply()
				if err != nil && err != Nil {
					return nil, err
				}

				cmd.val = append(cmd.val, XPendingExt{
					ID:         id,
					Consumer:   consumer,
					Idle:       time.Duration(idle) * time.Millisecond,
					RetryCount: retryCount,
				})
				return nil, nil
			})
			if err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	return cmd.err
}

//------------------------------------------------------------------------------

type XInfoGroupsCmd struct {
	baseCmd
	val []XInfoGroups
}

type XInfoGroups struct {
	Name            string
	Consumers       int64
	Pending         int64
	LastDeliveredID string
}

var _ Cmder = (*XInfoGroupsCmd)(nil)

func NewXInfoGroupsCmd(stream string) *XInfoGroupsCmd {
	return &XInfoGroupsCmd{
		baseCmd: baseCmd{args: []interface{}{"xinfo", "groups", stream}},
	}
}

func (cmd *XInfoGroupsCmd) Val() []XInfoGroups {
	return cmd.val
}

func (cmd *XInfoGroupsCmd) Result() ([]XInfoGroups, error) {
	return cmd.val, cmd.err
}

func (cmd *XInfoGroupsCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *XInfoGroupsCmd) readReply(rd *proto.Reader) error {
	_, cmd.err = rd.ReadArrayReply(
		func(rd *proto.Reader, n int64) (interface{}, error) {
			for i := int64(0); i < n; i++ {
				v, err := rd.ReadReply(xGroupInfoParser)
				if err != nil {
					return nil, err
				}
				cmd.val = append(cmd.val, v.(XInfoGroups))
			}
			return nil, nil
		})
	return nil
}

func xGroupInfoParser(rd *proto.Reader, n int64) (interface{}, error) {
	if n != 8 {
		return nil, fmt.Errorf("redis: got %d elements in XINFO GROUPS reply,"+
			"wanted 8", n)
	}
	var (
		err error
		grp XInfoGroups
		key string
		val string
	)

	for i := 0; i < 4; i++ {
		key, err = rd.ReadString()
		if err != nil {
			return nil, err
		}
		val, err = rd.ReadString()
		if err != nil {
			return nil, err
		}
		switch key {
		case "name":
			grp.Name = val
		case "consumers":
			grp.Consumers, err = strconv.ParseInt(val, 0, 64)
		case "pending":
			grp.Pending, err = strconv.ParseInt(val, 0, 64)
		case "last-delivered-id":
			grp.LastDeliveredID = val
		default:
			return nil, fmt.Errorf("redis: unexpected content %s "+
				"in XINFO GROUPS reply", key)
		}
		if err != nil {
			return nil, err
		}
	}
	return grp, err
}

//------------------------------------------------------------------------------

type ZSliceCmd struct {
	baseCmd

	val []Z
}

var _ Cmder = (*ZSliceCmd)(nil)

func NewZSliceCmd(args ...interface{}) *ZSliceCmd {
	return &ZSliceCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *ZSliceCmd) Val() []Z {
	return cmd.val
}

func (cmd *ZSliceCmd) Result() ([]Z, error) {
	return cmd.val, cmd.err
}

func (cmd *ZSliceCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *ZSliceCmd) readReply(rd *proto.Reader) error {
	_, cmd.err = rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
		cmd.val = make([]Z, n/2)
		for i := 0; i < len(cmd.val); i++ {
			member, err := rd.ReadString()
			if err != nil {
				return nil, err
			}

			score, err := rd.ReadFloatReply()
			if err != nil {
				return nil, err
			}

			cmd.val[i] = Z{
				Member: member,
				Score:  score,
			}
		}
		return nil, nil
	})
	return cmd.err
}

//------------------------------------------------------------------------------

type ZWithKeyCmd struct {
	baseCmd

	val *ZWithKey
}

var _ Cmder = (*ZWithKeyCmd)(nil)

func NewZWithKeyCmd(args ...interface{}) *ZWithKeyCmd {
	return &ZWithKeyCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *ZWithKeyCmd) Val() *ZWithKey {
	return cmd.val
}

func (cmd *ZWithKeyCmd) Result() (*ZWithKey, error) {
	return cmd.Val(), cmd.Err()
}

func (cmd *ZWithKeyCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *ZWithKeyCmd) readReply(rd *proto.Reader) error {
	_, cmd.err = rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
		if n != 3 {
			return nil, fmt.Errorf("got %d elements, expected 3", n)
		}

		cmd.val = &ZWithKey{}
		var err error

		cmd.val.Key, err = rd.ReadString()
		if err != nil {
			return nil, err
		}

		cmd.val.Member, err = rd.ReadString()
		if err != nil {
			return nil, err
		}

		cmd.val.Score, err = rd.ReadFloatReply()
		if err != nil {
			return nil, err
		}

		return nil, nil
	})
	return cmd.err
}

//------------------------------------------------------------------------------

type ScanCmd struct {
	baseCmd

	page   []string
	cursor uint64

	process func(cmd Cmder) error
}

var _ Cmder = (*ScanCmd)(nil)

func NewScanCmd(process func(cmd Cmder) error, args ...interface{}) *ScanCmd {
	return &ScanCmd{
		baseCmd: baseCmd{args: args},
		process: process,
	}
}

func (cmd *ScanCmd) Val() (keys []string, cursor uint64) {
	return cmd.page, cmd.cursor
}

func (cmd *ScanCmd) Result() (keys []string, cursor uint64, err error) {
	return cmd.page, cmd.cursor, cmd.err
}

func (cmd *ScanCmd) String() string {
	return cmdString(cmd, cmd.page)
}

func (cmd *ScanCmd) readReply(rd *proto.Reader) error {
	cmd.page, cmd.cursor, cmd.err = rd.ReadScanReply()
	return cmd.err
}

// Iterator creates a new ScanIterator.
func (cmd *ScanCmd) Iterator() *ScanIterator {
	return &ScanIterator{
		cmd: cmd,
	}
}

//------------------------------------------------------------------------------

type ClusterNode struct {
	ID   string
	Addr string
}

type ClusterSlot struct {
	Start int
	End   int
	Nodes []ClusterNode
}

type ClusterSlotsCmd struct {
	baseCmd

	val []ClusterSlot
}

var _ Cmder = (*ClusterSlotsCmd)(nil)

func NewClusterSlotsCmd(args ...interface{}) *ClusterSlotsCmd {
	return &ClusterSlotsCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *ClusterSlotsCmd) Val() []ClusterSlot {
	return cmd.val
}

func (cmd *ClusterSlotsCmd) Result() ([]ClusterSlot, error) {
	return cmd.Val(), cmd.Err()
}

func (cmd *ClusterSlotsCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *ClusterSlotsCmd) readReply(rd *proto.Reader) error {
	_, cmd.err = rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
		cmd.val = make([]ClusterSlot, n)
		for i := 0; i < len(cmd.val); i++ {
			n, err := rd.ReadArrayLen()
			if err != nil {
				return nil, err
			}
			if n < 2 {
				err := fmt.Errorf("redis: got %d elements in cluster info, expected at least 2", n)
				return nil, err
			}

			start, err := rd.ReadIntReply()
			if err != nil {
				return nil, err
			}

			end, err := rd.ReadIntReply()
			if err != nil {
				return nil, err
			}

			nodes := make([]ClusterNode, n-2)
			for j := 0; j < len(nodes); j++ {
				n, err := rd.ReadArrayLen()
				if err != nil {
					return nil, err
				}
				if n != 2 && n != 3 {
					err := fmt.Errorf("got %d elements in cluster info address, expected 2 or 3", n)
					return nil, err
				}

				ip, err := rd.ReadString()
				if err != nil {
					return nil, err
				}

				port, err := rd.ReadString()
				if err != nil {
					return nil, err
				}

				nodes[j].Addr = net.JoinHostPort(ip, port)

				if n == 3 {
					id, err := rd.ReadString()
					if err != nil {
						return nil, err
					}
					nodes[j].ID = id
				}
			}

			cmd.val[i] = ClusterSlot{
				Start: int(start),
				End:   int(end),
				Nodes: nodes,
			}
		}
		return nil, nil
	})
	return cmd.err
}

//------------------------------------------------------------------------------

// GeoLocation is used with GeoAdd to add geospatial location.
type GeoLocation struct {
	Name                      string
	Longitude, Latitude, Dist float64
	GeoHash                   int64
}

// GeoRadiusQuery is used with GeoRadius to query geospatial index.
type GeoRadiusQuery struct {
	Radius float64
	// Can be m, km, ft, or mi. Default is km.
	Unit        string
	WithCoord   bool
	WithDist    bool
	WithGeoHash bool
	Count       int
	// Can be ASC or DESC. Default is no sort order.
	Sort      string
	Store     string
	StoreDist string
}

type GeoLocationCmd struct {
	baseCmd

	q         *GeoRadiusQuery
	locations []GeoLocation
}

var _ Cmder = (*GeoLocationCmd)(nil)

func NewGeoLocationCmd(q *GeoRadiusQuery, args ...interface{}) *GeoLocationCmd {
	return &GeoLocationCmd{
		baseCmd: baseCmd{args: geoLocationArgs(q, args...)},
		q:       q,
	}
}

func geoLocationArgs(q *GeoRadiusQuery, args ...interface{}) []interface{} {
	args = append(args, q.Radius)
	if q.Unit != "" {
		args = append(args, q.Unit)
	} else {
		args = append(args, "km")
	}
	if q.WithCoord {
		args = append(args, "withcoord")
	}
	if q.WithDist {
		args = append(args, "withdist")
	}
	if q.WithGeoHash {
		args = append(args, "withhash")
	}
	if q.Count > 0 {
		args = append(args, "count", q.Count)
	}
	if q.Sort != "" {
		args = append(args, q.Sort)
	}
	if q.Store != "" {
		args = append(args, "store")
		args = append(args, q.Store)
	}
	if q.StoreDist != "" {
		args = append(args, "storedist")
		args = append(args, q.StoreDist)
	}
	return args
}

func (cmd *GeoLocationCmd) Val() []GeoLocation {
	return cmd.locations
}

func (cmd *GeoLocationCmd) Result() ([]GeoLocation, error) {
	return cmd.locations, cmd.err
}

func (cmd *GeoLocationCmd) String() string {
	return cmdString(cmd, cmd.locations)
}

func (cmd *GeoLocationCmd) readReply(rd *proto.Reader) error {
	var v interface{}
	v, cmd.err = rd.ReadArrayReply(newGeoLocationSliceParser(cmd.q))
	if cmd.err != nil {
		return cmd.err
	}
	cmd.locations = v.([]GeoLocation)
	return nil
}

func newGeoLocationSliceParser(q *GeoRadiusQuery) proto.MultiBulkParse {
	return func(rd *proto.Reader, n int64) (interface{}, error) {
		locs := make([]GeoLocation, 0, n)
		for i := int64(0); i < n; i++ {
			v, err := rd.ReadReply(newGeoLocationParser(q))
			if err != nil {
				return nil, err
			}
			switch vv := v.(type) {
			case string:
				locs = append(locs, GeoLocation{
					Name: vv,
				})
			case *GeoLocation:
				//TODO: avoid copying
				locs = append(locs, *vv)
			default:
				return nil, fmt.Errorf("got %T, expected string or *GeoLocation", v)
			}
		}
		return locs, nil
	}
}

func newGeoLocationParser(q *GeoRadiusQuery) proto.MultiBulkParse {
	return func(rd *proto.Reader, n int64) (interface{}, error) {
		var loc GeoLocation
		var err error

		loc.Name, err = rd.ReadString()
		if err != nil {
			return nil, err
		}
		if q.WithDist {
			loc.Dist, err = rd.ReadFloatReply()
			if err != nil {
				return nil, err
			}
		}
		if q.WithGeoHash {
			loc.GeoHash, err = rd.ReadIntReply()
			if err != nil {
				return nil, err
			}
		}
		if q.WithCoord {
			n, err := rd.ReadArrayLen()
			if err != nil {
				return nil, err
			}
			if n != 2 {
				return nil, fmt.Errorf("got %d coordinates, expected 2", n)
			}

			loc.Longitude, err = rd.ReadFloatReply()
			if err != nil {
				return nil, err
			}
			loc.Latitude, err = rd.ReadFloatReply()
			if err != nil {
				return nil, err
			}
		}

		return &loc, nil
	}
}

//------------------------------------------------------------------------------

type GeoPos struct {
	Longitude, Latitude float64
}

type GeoPosCmd struct {
	baseCmd

	val []*GeoPos
}

var _ Cmder = (*GeoPosCmd)(nil)

func NewGeoPosCmd(args ...interface{}) *GeoPosCmd {
	return &GeoPosCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *GeoPosCmd) Val() []*GeoPos {
	return cmd.val
}

func (cmd *GeoPosCmd) Result() ([]*GeoPos, error) {
	return cmd.Val(), cmd.Err()
}

func (cmd *GeoPosCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *GeoPosCmd) readReply(rd *proto.Reader) error {
	_, cmd.err = rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
		cmd.val = make([]*GeoPos, n)
		for i := 0; i < len(cmd.val); i++ {
			i := i
			_, err := rd.ReadReply(func(rd *proto.Reader, n int64) (interface{}, error) {
				longitude, err := rd.ReadFloatReply()
				if err != nil {
					return nil, err
				}

				latitude, err := rd.ReadFloatReply()
				if err != nil {
					return nil, err
				}

				cmd.val[i] = &GeoPos{
					Longitude: longitude,
					Latitude:  latitude,
				}
				return nil, nil
			})
			if err != nil {
				if err == Nil {
					cmd.val[i] = nil
					continue
				}
				return nil, err
			}
		}
		return nil, nil
	})
	return cmd.err

}

//------------------------------------------------------------------------------

type CommandInfo struct {
	Name        string
	Arity       int8
	Flags       []string
	FirstKeyPos int8
	LastKeyPos  int8
	StepCount   int8
	ReadOnly    bool
}

type CommandsInfoCmd struct {
	baseCmd

	val map[string]*CommandInfo
}

var _ Cmder = (*CommandsInfoCmd)(nil)

func NewCommandsInfoCmd(args ...interface{}) *CommandsInfoCmd {
	return &CommandsInfoCmd{
		baseCmd: baseCmd{args: args},
	}
}

func (cmd *CommandsInfoCmd) Val() map[string]*CommandInfo {
	return cmd.val
}

func (cmd *CommandsInfoCmd) Result() (map[string]*CommandInfo, error) {
	return cmd.Val(), cmd.Err()
}

func (cmd *CommandsInfoCmd) String() string {
	return cmdString(cmd, cmd.val)
}

func (cmd *CommandsInfoCmd) readReply(rd *proto.Reader) error {
	_, cmd.err = rd.ReadArrayReply(func(rd *proto.Reader, n int64) (interface{}, error) {
		cmd.val = make(map[string]*CommandInfo, n)
		for i := int64(0); i < n; i++ {
			v, err := rd.ReadReply(commandInfoParser)
			if err != nil {
				return nil, err
			}
			vv := v.(*CommandInfo)
			cmd.val[vv.Name] = vv
		}
		return nil, nil
	})
	return cmd.err
}

func commandInfoParser(rd *proto.Reader, n int64) (interface{}, error) {
	if n != 6 {
		return nil, fmt.Errorf("redis: got %d elements in COMMAND reply, wanted 6", n)
	}

	var cmd CommandInfo
	var err error

	cmd.Name, err = rd.ReadString()
	if err != nil {
		return nil, err
	}

	arity, err := rd.ReadIntReply()
	if err != nil {
		return nil, err
	}
	cmd.Arity = int8(arity)

	_, err = rd.ReadReply(func(rd *proto.Reader, n int64) (interface{}, error) {
		cmd.Flags = make([]string, n)
		for i := 0; i < len(cmd.Flags); i++ {
			switch s, err := rd.ReadString(); {
			case err == Nil:
				cmd.Flags[i] = ""
			case err != nil:
				return nil, err
			default:
				cmd.Flags[i] = s
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, err
	}

	firstKeyPos, err := rd.ReadIntReply()
	if err != nil {
		return nil, err
	}
	cmd.FirstKeyPos = int8(firstKeyPos)

	lastKeyPos, err := rd.ReadIntReply()
	if err != nil {
		return nil, err
	}
	cmd.LastKeyPos = int8(lastKeyPos)

	stepCount, err := rd.ReadIntReply()
	if err != nil {
		return nil, err
	}
	cmd.StepCount = int8(stepCount)

	for _, flag := range cmd.Flags {
		if flag == "readonly" {
			cmd.ReadOnly = true
			break
		}
	}

	return &cmd, nil
}

//------------------------------------------------------------------------------

type cmdsInfoCache struct {
	fn func() (map[string]*CommandInfo, error)

	once internal.Once
	cmds map[string]*CommandInfo
}

func newCmdsInfoCache(fn func() (map[string]*CommandInfo, error)) *cmdsInfoCache {
	return &cmdsInfoCache{
		fn: fn,
	}
}

func (c *cmdsInfoCache) Get() (map[string]*CommandInfo, error) {
	err := c.once.Do(func() error {
		cmds, err := c.fn()
		if err != nil {
			return err
		}
		for key := range cmds {
			lowerKey := internal.ToLower(key)
			if key != lowerKey {
				cmds[lowerKey] = cmds[key]
			}
		}
		c.cmds = cmds
		return nil
	})
	return c.cmds, err
}
