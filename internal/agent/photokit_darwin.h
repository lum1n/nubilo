#ifndef NUBILO_PHOTOKIT_H
#define NUBILO_PHOTOKIT_H

/* Returns malloc'd status string: authorized|limited|denied|restricted|not_determined|unknown */
char *nubilo_pk_auth_status(void);
/* Request Photos access; returns 1 if authorized or limited. Sets *err on failure. */
int nubilo_pk_request_access(char **err);
char *nubilo_pk_list_albums(char **err);
char *nubilo_pk_list_assets(const char *source, const char *album_ids_json, double after, double before, char **err);
/* IDs may be album localIdentifiers or "person:<localIdentifier>" for People & Pets. */
/* Export primary original (photo/RAW/video). Network access allowed; timed wait. */
int nubilo_pk_export_original(const char *local_id, const char *dest_path, char **err);
/* Export Live Photo paired movie when present; returns 0 if none. */
int nubilo_pk_export_live_movie(const char *local_id, const char *dest_path, char **err);
/* Import bytes from path into Photos library; optional album_id (empty = library only). */
int nubilo_pk_import(const char *src_path, const char *album_id, const char *filename, char **out_local_id, char **err);

#endif
