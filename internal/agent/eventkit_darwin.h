#ifndef NUBILO_EVENTKIT_H
#define NUBILO_EVENTKIT_H

char *nubilo_ek_list_calendars(char **err);
char *nubilo_ek_list_events(const char *calendar_id, double start, double end, char **err);
char *nubilo_ek_save_event(const char *calendar_id, const char *item_id, const char *json, char **err);
int nubilo_ek_delete_event(const char *item_id, char **err);

char *nubilo_ek_list_reminder_lists(char **err);
char *nubilo_ek_list_reminders(const char *calendar_id, double start, double end, char **err);
char *nubilo_ek_save_reminder(const char *calendar_id, const char *item_id, const char *json, char **err);
int nubilo_ek_delete_reminder(const char *item_id, char **err);

/* Register for EKEventStoreChangedNotification; cb may be called on any thread. */
void nubilo_ek_watch_changes(void (*cb)(void));

#endif
