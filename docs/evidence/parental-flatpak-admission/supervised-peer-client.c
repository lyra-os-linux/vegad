/* Disposable VM-only client. The binary is labeled for one child domain. */
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

static int write_all(int fd, const unsigned char *data, size_t length) {
    while (length != 0) {
        ssize_t count = write(fd, data, length);
        if (count < 0 && errno == EINTR) continue;
        if (count <= 0) return -1;
        data += count;
        length -= (size_t)count;
    }
    return 0;
}

int main(int argc, char **argv) {
    if (argc != 3 || (strcmp(argv[2], "valid") != 0 && strcmp(argv[2], "spoof") != 0))
        return 64;
    static const char valid[] = "{\"app_id\":\"org.lyra.TestApp\"}";
    static const char spoof[] = "{\"app_id\":\"org.lyra.TestApp\",\"uid\":0}";
    const char *payload = strcmp(argv[2], "valid") == 0 ? valid : spoof;
    size_t length = strlen(payload);
    if (length > 512 || strlen(argv[1]) >= sizeof(((struct sockaddr_un *)0)->sun_path))
        return 64;
    int fd = socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
    if (fd < 0) { perror("socket"); return 1; }
    struct sockaddr_un address = {.sun_family = AF_UNIX};
    strcpy(address.sun_path, argv[1]);
    if (connect(fd, (struct sockaddr *)&address, sizeof(address)) != 0) {
        perror("connect"); close(fd); return 2;
    }
    unsigned char header[2] = {(unsigned char)(length >> 8), (unsigned char)length};
    int result = write_all(fd, header, sizeof(header)) ||
                 write_all(fd, (const unsigned char *)payload, length);
    if (result) perror("write");
    close(fd);
    return result ? 3 : 0;
}
