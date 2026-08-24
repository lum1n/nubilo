#ifndef NUBILO_CONTACTS_H
#define NUBILO_CONTACTS_H

char *nubilo_cn_list(char **err);
char *nubilo_cn_save(const char *contact_id, const char *uid, const char *given, const char *family, const char *fn, const char *email, char **err);
int nubilo_cn_delete(const char *contact_id, char **err);

#endif
