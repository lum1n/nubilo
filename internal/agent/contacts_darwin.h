#ifndef NUBILO_CONTACTS_H
#define NUBILO_CONTACTS_H

char *nubilo_cn_list(char **err);
/* payload JSON: given, family, fn, org, nickname, note, emails[], phones[], addresses[], urls[], birthday, photo_b64 */
char *nubilo_cn_save(const char *contact_id, const char *payload_json, char **err);
int nubilo_cn_delete(const char *contact_id, char **err);
void nubilo_cn_watch_changes(void (*cb)(void));

#endif
