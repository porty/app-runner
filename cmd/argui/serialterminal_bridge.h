#pragma once

#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

void *serial_terminal_new(void *parent);
void *serial_terminal_widget(void *terminal);
int serial_terminal_feed(void *terminal, const char *data, int length);
int serial_terminal_read_input(void *terminal, char *data, int capacity);
void serial_terminal_clear(void *terminal);
void serial_terminal_focus(void *terminal);
void serial_terminal_copy(void *terminal);
void serial_terminal_paste(void *terminal);
void serial_terminal_delete(void *terminal);

#ifdef __cplusplus
}
#endif
