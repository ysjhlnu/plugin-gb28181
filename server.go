package b

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ghettovoice/gosip"
	"github.com/ghettovoice/gosip/log"
	"github.com/ghettovoice/gosip/sip"
	"github.com/logrusorgru/aurora"
	"go.uber.org/zap"
	"m7s.live/plugin/b/model"
	"m7s.live/plugin/b/utils"
)

var srv gosip.Server

const MaxRegisterCount = 3

func FindChannel(deviceId string, channelId string) (c *Channel) {
	if v, ok := Devices.Load(deviceId); ok {
		d := v.(*Device)
		if v, ok := d.channelMap.Load(channelId); ok {
			return v.(*Channel)
		}
	}
	return
}

var levelMap = map[string]log.Level{
	"trace": log.TraceLevel,
	"debug": log.DebugLevel,
	"info":  log.InfoLevel,
	"warn":  log.WarnLevel,
	"error": log.ErrorLevel,
	"fatal": log.FatalLevel,
	"panic": log.PanicLevel,
}

func GetSipServer(transport string) gosip.Server {
	return srv
}

var sn = 0

func CreateRequest(exposedId string, Method sip.RequestMethod, recipient *sip.Address, netAddr string) (req sip.Request) {

	sn++

	callId := sip.CallID(utils.RandNumString(10))
	userAgent := sip.UserAgentHeader("Monibuca")
	cseq := sip.CSeq{
		SeqNo:      uint32(sn),
		MethodName: Method,
	}
	port := sip.Port(conf.SipPort)
	serverAddr := sip.Address{
		//DisplayName: sip.String{Str: d.config.Serial},
		Uri: &sip.SipUri{
			FUser: sip.String{Str: exposedId},
			FHost: conf.SipIP,
			FPort: &port,
		},
		Params: sip.NewParams().Add("tag", sip.String{Str: utils.RandNumString(9)}),
	}
	req = sip.NewRequest(
		"",
		Method,
		recipient.Uri,
		"SIP/2.0",
		[]sip.Header{
			serverAddr.AsFromHeader(),
			recipient.AsToHeader(),
			&callId,
			&userAgent,
			&cseq,
			serverAddr.AsContactHeader(),
		},
		"",
		nil,
	)

	req.SetTransport(conf.SipNetwork)
	req.SetDestination(netAddr)
	//fmt.Printf("构建请求参数:%s", *&req)
	// requestMsg.DestAdd, err2 = d.ResolveAddress(requestMsg)
	// if err2 != nil {
	// 	return nil
	// }
	//intranet ip , let's resolve it with public ip
	// var deviceIp, deviceSourceIP net.IP
	// switch addr := requestMsg.DestAdd.(type) {
	// case *net.UDPAddr:
	// 	deviceIp = addr.IP
	// case *net.TCPAddr:
	// 	deviceIp = addr.IP
	// }

	// switch addr2 := d.SourceAddr.(type) {
	// case *net.UDPAddr:
	// 	deviceSourceIP = addr2.IP
	// case *net.TCPAddr:
	// 	deviceSourceIP = addr2.IP
	// }
	// if deviceIp.IsPrivate() && !deviceSourceIP.IsPrivate() {
	// 	requestMsg.DestAdd = d.SourceAddr
	// }
	return
}
func RequestForResponse(transport string, request sip.Request,
	options ...gosip.RequestWithContextOption) (sip.Response, error) {
	return (GetSipServer(transport)).RequestWithContext(context.Background(), request, options...)
}

func (c *BConfig) startServer() {
	addr := c.ListenAddr + ":" + strconv.Itoa(int(c.SipPort))

	logger := utils.NewZapLogger(BPlugin.Logger, "B-Interface SIP Server", nil)
	logger.SetLevel(uint32(levelMap[c.LogLevel]))
	// logger := log.NewDefaultLogrusLogger().WithPrefix("GB SIP Server")
	srvConf := gosip.ServerConfig{}
	if c.SipIP != "" {
		srvConf.Host = c.SipIP
	}
	srv = gosip.NewServer(srvConf, nil, nil, logger)
	srv.OnRequest(sip.REGISTER, c.OnRegister)
	srv.OnRequest(sip.MESSAGE, c.OnMessage)
	srv.OnRequest(sip.NOTIFY, c.OnNotify)
	srv.OnRequest(sip.BYE, c.OnBye)
	err := srv.Listen(strings.ToLower(c.SipNetwork), addr)
	if err != nil {
		BPlugin.Logger.Error("b-interface server listen", zap.Error(err))
	} else {
		BPlugin.Info(fmt.Sprint(aurora.Green("Server b-interface start at"), aurora.BrightBlue(addr)))
	}

	if c.MediaNetwork == "tcp" {
		c.tcpPorts.Init(c.MediaPortMin, c.MediaPortMax)
	} else {
		c.udpPorts.Init(c.MediaPortMin, c.MediaPortMax)
	}
	go c.startJob()
}

// func queryCatalog(config *transaction.Config) {
// 	t := time.NewTicker(time.Duration(config.CatalogInterval) * time.Second)
// 	for range t.C {
// 		Devices.Range(func(key, value interface{}) bool {
// 			device := value.(*Device)
// 			if time.Since(device.UpdateTime) > time.Duration(config.RegisterValidity)*time.Second {
// 				Devices.Delete(key)
// 			} else if device.Channels != nil {
// 				go device.Catalog()
// 			}
// 			return true
// 		})
// 	}
// }

// 定时任务
func (c *BConfig) startJob() {
	statusTick := time.NewTicker(c.HeartbeatInterval / 2)
	banTick := time.NewTicker(c.RemoveBanInterval)
	linkTick := time.NewTicker(time.Millisecond * 100)
	BPlugin.Debug("start job")
	for {
		select {
		case <-banTick.C:
			if c.Username != "" || c.Password != "" {
				c.removeBanDevice()
			}
		case <-statusTick.C:
			c.statusCheck()
		case <-linkTick.C:
			RecordQueryLink.cleanTimeout()
		}
	}
}

func (c *BConfig) removeBanDevice() {
	DeviceRegisterCount.Range(func(key, value interface{}) bool {
		if value.(int) > MaxRegisterCount {
			DeviceRegisterCount.Delete(key)
		}
		return true
	})
}

// statusCheck
// -  当设备超过 3 倍心跳时间未发送过心跳（通过 UpdateTime 判断）, 视为离线
// - 	当设备超过注册有效期内为发送过消息，则从设备列表中删除
// UpdateTime 在设备发送心跳之外的消息也会被更新，相对于 LastKeepaliveAt 更能体现出设备最会一次活跃的时间
func (c *BConfig) statusCheck() {
	Devices.Range(func(key, value any) bool {
		d := value.(*Device)
		if time.Since(d.UpdateTime) > c.RegisterValidity {
			if d.Status == DeviceOfflineStatus {
				return true
			}

			d.Online = false
			d.Status = DeviceOfflineStatus
			if err := model.UpdateDeviceStatus(BPlugin.DB, BPlugin.Name, d.ID, string(d.Status), false); err != nil {
				BPlugin.Error(err.Error())
			}

			//Devices.Delete(key)
			BPlugin.Info("Device register timeout",
				zap.String("id", d.ID),
				zap.Time("registerTime", d.RegisterTime),
				zap.Time("updateTime", d.UpdateTime),
			)
		} else if time.Since(d.UpdateTime) > c.HeartbeatInterval*3 {
			if d.Status == DeviceOfflineStatus {
				return true
			}
			d.Online = false
			d.Status = DeviceOfflineStatus

			if err := model.UpdateDeviceStatus(BPlugin.DB, BPlugin.Name, d.ID, string(d.Status), false); err != nil {
				BPlugin.Error(err.Error())
			}
			d.channelMap.Range(func(key, value any) bool {
				ch := value.(*Channel)
				ch.Online = false
				ch.Status = ChannelOffStatus
				if err := model.UpdateDeviceChannelStatus(BPlugin.DB, BPlugin.Name, d.ID, ch.DeviceID, string(ch.Status)); err != nil {
					BPlugin.Error(err.Error())
				}
				return true
			})
			BPlugin.Info("Device offline", zap.String("id", d.ID), zap.Time("updateTime", d.UpdateTime))
		}
		return true
	})
}
