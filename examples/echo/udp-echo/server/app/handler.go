/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

import (
	getty "github.com/apache/dubbo-getty"
)

const (
	WritePkgTimeout = 1e8
	WritePkgASAP    = 0e9
)

var (
	errTooManySessions = errors.New("Too many echo sessions!")
	hbHandler          = &HeartbeatHandler{}
	msgHandler         = &MessageHandler{}
	echoMsgHandler     = newEchoMessageHandler()
)

type PackageHandler interface {
	Handle(getty.Session, getty.UDPContext) error
}

////////////////////////////////////////////
// heartbeat handler
////////////////////////////////////////////

type HeartbeatHandler struct{}

func (h *HeartbeatHandler) Handle(session getty.Session, ctx getty.UDPContext) error {
	var (
		ok     bool
		pkg    *EchoPackage
		rspPkg EchoPackage
	)

	log.Debugf("get echo heartbeat udp context{%#v}", ctx)
	if pkg, ok = ctx.Pkg.(*EchoPackage); !ok {
		return fmt.Errorf("illegal @ctx.Pkg:%#v", ctx.Pkg)
	}

	rspPkg.H = pkg.H
	rspPkg.B = echoHeartbeatResponseString
	rspPkg.H.Len = uint16(len(rspPkg.B) + 1)

	// return session.WritePkg(getty.UDPContext{Pkg: &rspPkg, PeerAddr: ctx.PeerAddr}, WritePkgTimeout)
	_, _, err := session.WritePkg(getty.UDPContext{Pkg: &rspPkg, PeerAddr: ctx.PeerAddr}, WritePkgASAP)
	if err != nil {
		log.Warnf("session.WritePkg(session{%s}, pkg{%s}) = error{%v}", session.Stat(), pkg, err)
		session.Close()
	}
	return err
}

////////////////////////////////////////////
// message handler
////////////////////////////////////////////

type MessageHandler struct{}

func (h *MessageHandler) Handle(session getty.Session, ctx getty.UDPContext) error {
	log.Debugf("get echo ctx{%#v}", ctx)
	// write echo message handle logic here.
	// return session.WritePkg(ctx, WritePkgTimeout)
	_, _, err := session.WritePkg(ctx, WritePkgASAP)
	return err
}

////////////////////////////////////////////
// EchoMessageHandler
////////////////////////////////////////////

type clientEchoSession struct {
	session getty.Session
	reqNum  int32
}

type EchoMessageHandler struct {
	handlers map[uint32]PackageHandler

	rwlock     sync.RWMutex
	sessionMap map[getty.Session]*clientEchoSession
}

func newEchoMessageHandler() *EchoMessageHandler {
	handlers := make(map[uint32]PackageHandler)
	handlers[heartbeatCmd] = hbHandler
	handlers[echoCmd] = msgHandler

	return &EchoMessageHandler{sessionMap: make(map[getty.Session]*clientEchoSession), handlers: handlers}
}

func (h *EchoMessageHandler) OnOpen(session getty.Session) error {
	var err error

	h.rwlock.RLock()
	if conf.SessionNumber <= len(h.sessionMap) {
		err = errTooManySessions
	}
	h.rwlock.RUnlock()
	if err != nil {
		return err
	}

	log.Infof("got session:%s", session.Stat())
	h.rwlock.Lock()
	h.sessionMap[session] = &clientEchoSession{session: session}
	h.rwlock.Unlock()
	return nil
}

func (h *EchoMessageHandler) OnError(session getty.Session, err error) {
	log.Infof("session{%s} got error{%v}, will be closed.", session.Stat(), err)
	h.rwlock.Lock()
	delete(h.sessionMap, session)
	h.rwlock.Unlock()
}

func (h *EchoMessageHandler) OnClose(session getty.Session) {
	log.Infof("session{%s} is closing......", session.Stat())
	h.rwlock.Lock()
	delete(h.sessionMap, session)
	h.rwlock.Unlock()
}

func (h *EchoMessageHandler) OnMessage(session getty.Session, udpCtx interface{}) {
	ctx, ok := udpCtx.(getty.UDPContext)
	if !ok {
		log.Error("illegal UDPContext{%#v}", udpCtx)
		return
	}

	p, ok := ctx.Pkg.(*EchoPackage)
	if !ok {
		log.Errorf("illegal pkg{%#v}", ctx.Pkg)
		return
	}

	handler, ok := h.handlers[p.H.Command]
	if !ok {
		log.Errorf("illegal command{%d}", p.H.Command)
		return
	}
	err := handler.Handle(session, ctx)
	if err != nil {
		h.rwlock.Lock()
		if _, ok := h.sessionMap[session]; ok {
			h.sessionMap[session].reqNum++
		}
		h.rwlock.Unlock()
	}
}

func (h *EchoMessageHandler) OnCron(session getty.Session) {
	// flag   bool
	var active time.Time
	h.rwlock.RLock()
	if _, ok := h.sessionMap[session]; ok {
		active = session.GetActive()
		if conf.sessionTimeout.Nanoseconds() < time.Since(active).Nanoseconds() {
			// flag = true
			log.Errorf("session{%s} timeout{%s}, reqNum{%d}",
				session.Stat(), time.Since(active).String(), h.sessionMap[session].reqNum)
		}
	}
	h.rwlock.RUnlock()
	// udp session是根据本地udp socket fd生成的，如果关闭则连同socket也一同关闭了
	//if flag {
	//	h.rwlock.Lock()
	//	delete(h.sessionMap, session)
	//	h.rwlock.Unlock()
	//	session.Close()
	//}
}
