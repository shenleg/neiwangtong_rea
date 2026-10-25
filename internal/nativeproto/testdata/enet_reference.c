/* 本文件是可选 ENet 1.3.18 官方库的 loopback 互通驱动；只连接或绑定 127.0.0.1，不含真实身份或业务消息。 */
#include <enet/enet.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/* pattern 生成确定性合成字节；index 是消息偏移，返回该位置字节，不访问外部资源。 */
static unsigned char pattern(size_t index) { return (unsigned char)((index * 17 + 31) % 251); }

/* send_pattern 在 peer 上排队一次固定合成消息；返回零表示排队成功，失败不重提。 */
static int send_pattern(ENetPeer *peer) {
    size_t length = 70003;
    ENetPacket *packet = enet_packet_create(NULL, length, ENET_PACKET_FLAG_RELIABLE);
    if (!packet) return -1;
    for (size_t i = 0; i < length; ++i) packet->data[i] = pattern(i);
    if (enet_peer_send(peer, 0, packet) == 0) return 0;
    enet_packet_destroy(packet);
    return -1;
}

/* main 的 server 模式绑定 loopback 临时端口并回显，client 模式只连接参数指定的 loopback 端口。
 * 成功时返回 0，参数/初始化/超时/收发错误返回非零；只用于合成互通测试，不提供任意远端选择。 */
int main(int argc, char **argv) {
    if (argc < 2 || (strcmp(argv[1], "server") && strcmp(argv[1], "client"))) return 2;
    int client = !strcmp(argv[1], "client");
    if (client && argc != 3) return 2;
    char *end = NULL;
    unsigned long port = client ? strtoul(argv[2], &end, 10) : 0;
    if (client && (!end || *end || !port || port > 65535)) return 2;
    if (enet_initialize() != 0) return 3;
    ENetAddress address;
    if (enet_address_set_host_ip(&address, "127.0.0.1") != 0) return 3;
    address.port = 0;
    ENetHost *host = enet_host_create(&address, 1, 1, 0, 0);
    if (!host) return 3;
    host->maximumPacketSize = 1024 * 1024;
    host->maximumWaitingData = 2 * 1024 * 1024;
    if (client) {
        address.port = (enet_uint16)port;
        if (!enet_host_connect(host, &address, 1, 0)) return 3;
    }
    printf("READY %u\n", host->address.port);
    fflush(stdout);
    ENetEvent event;
    enet_uint32 started = enet_time_get();
    int result = 4;
    unsigned int received = 0;
    while (enet_time_get() - started < 10000) {
        int got = enet_host_service(host, &event, 10);
        if (got < 0) break;
        if (!got) continue;
        if (event.type == ENET_EVENT_TYPE_CONNECT) {
            if (client && send_pattern(event.peer) != 0) break;
        } else if (event.type == ENET_EVENT_TYPE_RECEIVE) {
            if (++received != 1) { enet_packet_destroy(event.packet); result = 6; break; }
            if (client) {
                int valid = event.packet->dataLength == 70003;
                for (size_t i = 0; valid && i < event.packet->dataLength; ++i)
                    valid = event.packet->data[i] == pattern(i);
                enet_packet_destroy(event.packet);
                if (!valid) { result = 5; break; }
                /* 先发出 echo 的接收 ACK，再发 DISCONNECT，避免测试将已收到但未确认的发送当成成功。 */
                enet_host_flush(host);
                enet_peer_disconnect(event.peer, 0);
            } else {
                if (event.packet->dataLength > 1024 * 1024) { enet_packet_destroy(event.packet); break; }
                ENetPacket *echo = enet_packet_create(event.packet->data, event.packet->dataLength, ENET_PACKET_FLAG_RELIABLE);
                enet_packet_destroy(event.packet);
                if (!echo) break;
                if (enet_peer_send(event.peer, 0, echo) != 0) { enet_packet_destroy(echo); break; }
            }
        } else if (event.type == ENET_EVENT_TYPE_DISCONNECT) {
            result = 0;
            break;
        }
    }
    enet_host_destroy(host);
    enet_deinitialize();
    return result;
}
