// Syscall-level micro-benchmarks run inside each sandbox. Prints one JSON
// object: {"bench": ..., "ops": N, "ns_per_op": X}.
#define _GNU_SOURCE
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

static double now(void) {
	struct timespec t;
	clock_gettime(CLOCK_MONOTONIC, &t);
	return t.tv_sec * 1e9 + t.tv_nsec;
}

static void out(const char *b, long ops, double ns) {
	printf("{\"bench\":\"%s\",\"ops\":%ld,\"ns_per_op\":%.1f}\n", b, ops, ns / ops);
}

int main(int argc, char **argv) {
	if (argc < 3) {
		fprintf(stderr, "usage: micro getpid|stat|openclose|forkexec|pipe|write|read N [path]\n");
		return 2;
	}
	const char *b = argv[1];
	long n = atol(argv[2]);
	const char *path = argc > 3 ? argv[3] : "/tmp/micro.dat";
	double t0;
	if (!strcmp(b, "getpid")) {
		t0 = now();
		for (long i = 0; i < n; i++) syscall(SYS_getpid); // raw syscall, no libc caching
		out(b, n, now() - t0);
	} else if (!strcmp(b, "stat") || !strcmp(b, "openclose")) {
		int fd = open(path, O_CREAT | O_WRONLY, 0644);
		if (fd < 0) { perror("open"); return 1; }
		close(fd);
		struct stat st;
		t0 = now();
		for (long i = 0; i < n; i++) {
			if (b[0] == 's') stat(path, &st);
			else close(open(path, O_RDONLY));
		}
		out(b, n, now() - t0);
		unlink(path);
	} else if (!strcmp(b, "forkexec")) {
		t0 = now();
		for (long i = 0; i < n; i++) {
			pid_t p = fork();
			if (p == 0) { execl("/bin/true", "true", (char *)0); _exit(127); }
			int s;
			waitpid(p, &s, 0);
		}
		out(b, n, now() - t0);
	} else if (!strcmp(b, "pipe")) { // round trips between two processes
		int a[2], c[2];
		char x = 0;
		if (pipe(a) || pipe(c)) { perror("pipe"); return 1; }
		pid_t p = fork();
		if (p == 0) {
			for (long i = 0; i < n; i++) { if (read(a[0], &x, 1) != 1) _exit(1); if (write(c[1], &x, 1) != 1) _exit(1); }
			_exit(0);
		}
		t0 = now();
		for (long i = 0; i < n; i++) { if (write(a[1], &x, 1) != 1) return 1; if (read(c[0], &x, 1) != 1) return 1; }
		out(b, n, now() - t0);
		waitpid(p, 0, 0);
	} else if (!strcmp(b, "write") || !strcmp(b, "read")) { // n MiB, 64 KiB blocks
		static char buf[1 << 16];
		memset(buf, 7, sizeof buf);
		long blocks = n * 16;
		if (b[0] == 'w') {
			int fd = open(path, O_CREAT | O_TRUNC | O_WRONLY, 0644);
			t0 = now();
			for (long i = 0; i < blocks; i++) if (write(fd, buf, sizeof buf) != sizeof buf) { perror("write"); return 1; }
			fsync(fd);
			close(fd);
		} else {
			int fd = open(path, O_RDONLY);
			if (fd < 0) { perror("open"); return 1; }
			t0 = now();
			while (read(fd, buf, sizeof buf) > 0) {}
			close(fd);
		}
		out(b, blocks, now() - t0);
	} else {
		fprintf(stderr, "unknown bench %s\n", b);
		return 2;
	}
	return 0;
}
