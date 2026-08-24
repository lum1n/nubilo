#ifndef NUBILO_EVENTKIT_H
#define NUBILO_EVENTKIT_H

char *nubilo_ek_list_calendars(char **err);
char *nubilo_ek_list_events(const char *calendar_id, double start, double end, char **err);
char *nubilo_ek_save_event(const char *calendar_id, const char *item_id, const char *uid, const char *title, const char *notes, const char *location, double start, double end, int all_day, char **err);
int nubilo_ek_delete_event(const char *item_id, char **err);

#endif
