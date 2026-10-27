#ifndef NUBILO_EVENTKIT_H
#define NUBILO_EVENTKIT_H

/* Read-only: the agent mirrors calendars to the server, it never writes to them. */
char *nubilo_ek_list_calendars(char **err);
char *nubilo_ek_list_events(const char *calendar_id, double start, double end, char **err);

/* Register for EKEventStoreChangedNotification; cb may be called on any thread. */
void nubilo_ek_watch_changes(void (*cb)(void));

#endif
