#ifndef NUBILO_CONTACTS_H
#define NUBILO_CONTACTS_H

char *nubilo_cn_list(char **err);
/* payload is JSON: given, family, fn, emails[], phones[], addresses[] */
char *nubilo_cn_save(const char *contact_id, const char *payload_json, char **err);
int nubilo_cn_delete(const char *contact_id, char **err);

#endif
