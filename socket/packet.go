// Copyright 2017 HenryLee. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package socket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sync"

	"github.com/henrylee2cn/goutil"
	"github.com/henrylee2cn/teleport/codec"
	"github.com/henrylee2cn/teleport/utils"
	"github.com/henrylee2cn/teleport/xfer"
)

type (
	// Packet a socket data packet.
	Packet struct {
		// packet sequence
		seq uint64
		// packet type, such as PULL, PUSH, REPLY
		ptype byte
		// URL string
		uri string
		// URL object
		url *url.URL
		// metadata
		meta *utils.Args
		// body codec type
		bodyCodec byte
		// body object
		body interface{}
		// newBodyFunc creates a new body by packet type and URI.
		// Note:
		//  only for writing packet;
		//  should be nil when reading packet.
		newBodyFunc NewBodyFunc
		// XferPipe transfer filter pipe, handlers from outer-most to inner-most.
		// Note: the length can not be bigger than 255!
		xferPipe *xfer.XferPipe
		// packet size
		size uint32
		// ctx is the packet handling context,
		// carries a deadline, a cancelation signal,
		// and other values across API boundaries.
		ctx context.Context
		// stack
		next *Packet
	}
	// Header packet header interface
	Header interface {
		// Ptype returns the packet sequence
		Seq() uint64
		// SetSeq sets the packet sequence
		SetSeq(uint64)
		// Ptype returns the packet type, such as PULL, PUSH, REPLY
		Ptype() byte
		// Ptype sets the packet type
		SetPtype(byte)
		// Uri returns the URI string
		Uri() string
		// SetUri sets the packet URI
		SetUri(string)
		// Url returns the URI object
		Url() *url.URL
		// Meta returns the metadata
		Meta() *utils.Args
	}
	// Body packet body interface
	Body interface {
		// BodyCodec returns the body codec type id
		BodyCodec() byte
		// SetBodyCodec sets the body codec type id
		SetBodyCodec(bodyCodec byte)
		// Body returns the body object
		Body() interface{}
		// SetBody sets the body object
		SetBody(body interface{})
		// SetNewBody resets the function of geting body.
		SetNewBody(newBodyFunc NewBodyFunc)
		// NewBody creates a new body by packet type and URI.
		// Note:
		//  only for writing packet;
		//  should be nil when reading packet.
		// NewBody(seq uint64, ptype byte, uri string) interface{}

		// MarshalBody returns the encoding of body.
		// Note: when the body is a stream of bytes, no marshalling is done.
		MarshalBody() ([]byte, error)
		// UnmarshalNewBody unmarshals the encoded data to a new body.
		// Note:
		// seq, ptype, uri must be setted already;
		// when the body is a stream of bytes, no unmarshalling is done.
		UnmarshalNewBody(bodyBytes []byte) error
		// UnmarshalBody unmarshals the encoded data to the existed body.
		// Note: when the body is a stream of bytes, no unmarshalling is done.
		UnmarshalBody(bodyBytes []byte) error
	}

	// NewBodyFunc creates a new body by header.
	NewBodyFunc func(Header) interface{}
)

var (
	_ Header = new(Packet)
	_ Body   = new(Packet)
)

var packetStack = new(struct {
	freePacket *Packet
	mu         sync.Mutex
})

// GetPacket gets a *Packet form packet stack.
// Note:
//  newBodyFunc is only for reading form connection;
//  settings are only for writing to connection.
func GetPacket(settings ...PacketSetting) *Packet {
	packetStack.mu.Lock()
	p := packetStack.freePacket
	if p == nil {
		p = NewPacket(settings...)
	} else {
		packetStack.freePacket = p.next
		p.doSetting(settings...)
	}
	packetStack.mu.Unlock()
	return p
}

// PutPacket puts a *Packet to packet stack.
func PutPacket(p *Packet) {
	packetStack.mu.Lock()
	p.Reset()
	p.next = packetStack.freePacket
	packetStack.freePacket = p
	packetStack.mu.Unlock()
}

// NewPacket creates a new *Packet.
// Note:
//  NewBody is only for reading form connection;
//  settings are only for writing to connection.
func NewPacket(settings ...PacketSetting) *Packet {
	var p = &Packet{
		meta:     new(utils.Args),
		xferPipe: new(xfer.XferPipe),
	}
	p.doSetting(settings...)
	return p
}

// Reset resets itself.
// Note:
//  newBodyFunc is only for reading form connection;
//  settings are only for writing to connection.
func (p *Packet) Reset(settings ...PacketSetting) {
	p.next = nil
	p.body = nil
	p.meta.Reset()
	p.xferPipe.Reset()
	p.newBodyFunc = nil
	p.seq = 0
	p.ptype = 0
	p.uri = ""
	p.url = nil
	p.size = 0
	p.ctx = nil
	p.bodyCodec = codec.NilCodecId
	p.doSetting(settings...)
}

func (p *Packet) doSetting(settings ...PacketSetting) {
	for _, fn := range settings {
		if fn != nil {
			fn(p)
		}
	}
}

// // HasContext returns true if the packet handling context is not nil.
// func (p *Packet) HasContext() bool {
// 	return p.ctx != nil
// }

// Context returns the packet handling context.
func (p *Packet) Context() context.Context {
	if p.ctx == nil {
		return context.Background()
	}
	return p.ctx
}

// Seq returns the packet sequence
func (p *Packet) Seq() uint64 {
	return p.seq
}

// SetSeq sets the packet sequence
func (p *Packet) SetSeq(seq uint64) {
	p.seq = seq
}

// Ptype returns the packet type, such as PULL, PUSH, REPLY
func (p *Packet) Ptype() byte {
	return p.ptype
}

// SetPtype sets the packet type
func (p *Packet) SetPtype(ptype byte) {
	p.ptype = ptype
}

// Uri returns the URI string
func (p *Packet) Uri() string {
	return p.uri
}

// SetUri sets the packet URI
func (p *Packet) SetUri(uri string) {
	p.uri = uri
	p.url = nil
}

// Url returns the URI object
func (p *Packet) Url() *url.URL {
	if p.url == nil {
		p.url, _ = url.Parse(p.uri)
		if p.url == nil {
			p.url = new(url.URL)
		}
	}
	return p.url
}

// Meta returns the metadata.
// When the package is reset, it will be reset.
func (p *Packet) Meta() *utils.Args {
	return p.meta
}

// BodyCodec returns the body codec type id
func (p *Packet) BodyCodec() byte {
	return p.bodyCodec
}

// SetBodyCodec sets the body codec type id
func (p *Packet) SetBodyCodec(bodyCodec byte) {
	p.bodyCodec = bodyCodec
}

// Body returns the body object
func (p *Packet) Body() interface{} {
	return p.body
}

// SetBody sets the body object
func (p *Packet) SetBody(body interface{}) {
	p.body = body
}

// SetNewBody resets the function of geting body.
func (p *Packet) SetNewBody(newBodyFunc NewBodyFunc) {
	p.newBodyFunc = newBodyFunc
}

// // NewBody creates a new body by packet type and URI.
// // Note:
// //  only for writing packet;
// //  should be nil when reading packet.
// func (p *Packet) NewBody(seq uint64, ptype byte, uri string) interface{} {
// 	return p.newBodyFunc(seq, ptype, uri)
// }

// MarshalBody returns the encoding of body.
// Note: when the body is a stream of bytes, no marshalling is done.
func (p *Packet) MarshalBody() ([]byte, error) {
	switch body := p.body.(type) {
	default:
		c, err := codec.Get(p.bodyCodec)
		if err != nil {
			return []byte{}, err
		}
		return c.Marshal(body)
	case nil:
		return []byte{}, nil
	case *[]byte:
		if body == nil {
			return []byte{}, nil
		}
		return *body, nil
	case []byte:
		return body, nil
	}
}

// UnmarshalNewBody unmarshals the encoded data to a new body.
// Note:
// seq, ptype, uri must be setted already;
// when the body is a stream of bytes, no unmarshalling is done.
func (p *Packet) UnmarshalNewBody(bodyBytes []byte) error {
	if p.newBodyFunc == nil {
		p.body = nil
		return nil
	}
	p.body = p.newBodyFunc(p)
	return p.UnmarshalBody(bodyBytes)
}

// UnmarshalBody unmarshals the encoded data to the existed body.
// Note: when the body is a stream of bytes, no unmarshalling is done.
func (p *Packet) UnmarshalBody(bodyBytes []byte) error {
	if len(bodyBytes) == 0 {
		return nil
	}
	switch body := p.body.(type) {
	default:
		c, err := codec.Get(p.bodyCodec)
		if err != nil {
			return err
		}
		return c.Unmarshal(bodyBytes, p.body)
	case nil:
		return nil
	case *[]byte:
		if body != nil {
			*body = make([]byte, len(bodyBytes))
			copy(*body, bodyBytes)
		}
		return nil
	}
}

// XferPipe returns transfer filter pipe, handlers from outer-most to inner-most.
// Note: the length can not be bigger than 255!
func (p *Packet) XferPipe() *xfer.XferPipe {
	return p.xferPipe
}

// AppendXferPipeFrom appends transfer filters from a *Packet.
func (p *Packet) AppendXferPipeFrom(src *Packet) {
	p.xferPipe.AppendFrom(src.xferPipe)
}

// Size returns the size of packet.
func (p *Packet) Size() uint32 {
	return p.size
}

// SetSize sets the size of packet.
// If the size is too big, returns error.
func (p *Packet) SetSize(size uint32) error {
	err := checkPacketSize(size)
	if err != nil {
		return err
	}
	p.size = size
	return nil
}

const packetFormat = `
{
  "seq": %d,
  "ptype": %d,
  "uri": %q,
  "meta": %q,
  "body_codec": %d,
  "body": %s,
  "xfer_pipe": %s,
  "size": %d
}`

// String returns printing text.
func (p *Packet) String() string {
	var xferPipeIds = make([]int, p.xferPipe.Len())
	for i, id := range p.xferPipe.Ids() {
		xferPipeIds[i] = int(id)
	}
	idsBytes, _ := json.Marshal(xferPipeIds)
	b, _ := json.Marshal(p.body)
	dst := bytes.NewBuffer(make([]byte, 0, len(b)*2))
	json.Indent(dst, goutil.StringToBytes(
		fmt.Sprintf(packetFormat,
			p.seq,
			p.ptype,
			p.uri,
			p.meta.QueryString(),
			p.bodyCodec,
			b,
			idsBytes,
			p.size,
		),
	), "", "  ")
	return goutil.BytesToString(dst.Bytes())
}

// PacketSetting sets Header field.
type PacketSetting func(*Packet)

// WithContext sets the packet handling context.
func WithContext(ctx context.Context) PacketSetting {
	return func(p *Packet) {
		p.ctx = ctx
	}
}

// WithSeq sets the packet sequence
func WithSeq(seq uint64) PacketSetting {
	return func(p *Packet) {
		p.seq = seq
	}
}

// WithPtype sets the packet type
func WithPtype(ptype byte) PacketSetting {
	return func(p *Packet) {
		p.ptype = ptype
	}
}

// WithUri sets the packet URL string
func WithUri(uri string) PacketSetting {
	return func(p *Packet) {
		p.uri = uri
		p.url = nil
	}
}

// WithQuery sets the packet URL query parameter.
func WithQuery(key, value string) PacketSetting {
	return func(p *Packet) {
		u := p.Url()
		v := u.Query()
		v.Add(key, value)
		u.RawQuery = v.Encode()
		p.uri = u.String()
		p.url = u
	}
}

// WithAddMeta adds 'key=value' metadata argument.
// Multiple values for the same key may be added.
func WithAddMeta(key, value string) PacketSetting {
	return func(p *Packet) {
		p.meta.Add(key, value)
	}
}

// WithSetMeta sets 'key=value' metadata argument.
func WithSetMeta(key, value string) PacketSetting {
	return func(p *Packet) {
		p.meta.Set(key, value)
	}
}

// WithBodyCodec sets the body codec
func WithBodyCodec(bodyCodec byte) PacketSetting {
	return func(p *Packet) {
		p.bodyCodec = bodyCodec
	}
}

// WithBody sets the body object
func WithBody(body interface{}) PacketSetting {
	return func(p *Packet) {
		p.body = body
	}
}

// WithNewBody resets the function of geting body.
func WithNewBody(newBodyFunc NewBodyFunc) PacketSetting {
	return func(p *Packet) {
		p.newBodyFunc = newBodyFunc
	}
}

// WithXferPipe sets transfer filter pipe.
func WithXferPipe(filterId ...byte) PacketSetting {
	return func(p *Packet) {
		p.xferPipe.Append(filterId...)
	}
}

var (
	packetSizeLimit uint32 = math.MaxUint32
	// ErrExceedPacketSizeLimit error
	ErrExceedPacketSizeLimit = errors.New("Size of package exceeds limit.")
)

// PacketSizeLimit gets the packet size upper limit of reading.
func PacketSizeLimit() uint32 {
	return packetSizeLimit
}

// SetPacketSizeLimit sets max packet size.
// If maxSize<=0, set it to max uint32.
func SetPacketSizeLimit(maxPacketSize uint32) {
	if maxPacketSize <= 0 {
		packetSizeLimit = math.MaxUint32
	} else {
		packetSizeLimit = maxPacketSize
	}
}

func checkPacketSize(packetSize uint32) error {
	if packetSize > packetSizeLimit {
		return ErrExceedPacketSizeLimit
	}
	return nil
}
