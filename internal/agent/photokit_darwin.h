#ifndef NUBILO_PHOTOKIT_H
#define NUBILO_PHOTOKIT_H

char *nubilo_pk_list_albums(char **err);
char *nubilo_pk_list_assets(const char *source, const char *album_ids_json, double after, double before, char **err);
int nubilo_pk_export_original(const char *local_id, const char *dest_path, char **err);

#endif
