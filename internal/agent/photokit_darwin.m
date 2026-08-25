//go:build darwin

#import "photokit_darwin.h"
#import <Photos/Photos.h>
#import <Foundation/Foundation.h>

static const NSTimeInterval kNubiloPKExportTimeout = 120.0;

static char *nubilo_pk_dup(NSString *s) {
	if (!s) {
		return strdup("");
	}
	const char *u = [s UTF8String];
	return strdup(u ? u : "");
}

static int nubilo_pk_wait(dispatch_semaphore_t sem, NSTimeInterval timeout) {
	NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:timeout];
	while (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 50 * NSEC_PER_MSEC))) {
		if ([deadline timeIntervalSinceNow] < 0) {
			return 0;
		}
		[[NSRunLoop currentRunLoop] runMode:NSDefaultRunLoopMode beforeDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
	}
	return 1;
}

static NSString *nubilo_pk_status_name(PHAuthorizationStatus st) {
	switch (st) {
	case PHAuthorizationStatusNotDetermined:
		return @"not_determined";
	case PHAuthorizationStatusRestricted:
		return @"restricted";
	case PHAuthorizationStatusDenied:
		return @"denied";
	case PHAuthorizationStatusAuthorized:
		return @"authorized";
	case PHAuthorizationStatusLimited:
		return @"limited";
	default:
		return @"unknown";
	}
}

static int nubilo_pk_access(NSError **outErr) {
	PHAuthorizationStatus st;
	if (@available(macOS 14.0, *)) {
		st = [PHPhotoLibrary authorizationStatusForAccessLevel:PHAccessLevelReadWrite];
		if (st == PHAuthorizationStatusAuthorized || st == PHAuthorizationStatusLimited) {
			return 1;
		}
	} else {
		st = [PHPhotoLibrary authorizationStatus];
		if (st == PHAuthorizationStatusAuthorized) {
			return 1;
		}
	}
	if (st == PHAuthorizationStatusDenied || st == PHAuthorizationStatusRestricted) {
		if (outErr) {
			NSString *msg = [NSString stringWithFormat:
				@"photos access %@ — enable Photos for your Terminal (or the nubilo binary) in System Settings → Privacy & Security → Photos; then: scripts/mac-sign.sh $(which nubilo)",
				nubilo_pk_status_name(st)];
			*outErr = [NSError errorWithDomain:@"nubilo" code:1 userInfo:@{NSLocalizedDescriptionKey: msg}];
		}
		return 0;
	}
	__block BOOL ok = NO;
	__block PHAuthorizationStatus got = st;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	if (@available(macOS 14.0, *)) {
		[PHPhotoLibrary requestAuthorizationForAccessLevel:PHAccessLevelReadWrite handler:^(PHAuthorizationStatus status) {
			got = status;
			ok = (status == PHAuthorizationStatusAuthorized || status == PHAuthorizationStatusLimited);
			dispatch_semaphore_signal(sem);
		}];
	} else {
		[PHPhotoLibrary requestAuthorization:^(PHAuthorizationStatus status) {
			got = status;
			ok = (status == PHAuthorizationStatusAuthorized);
			dispatch_semaphore_signal(sem);
		}];
	}
	if (!nubilo_pk_wait(sem, 60.0)) {
		if (outErr) {
			*outErr = [NSError errorWithDomain:@"nubilo" code:2 userInfo:@{
				NSLocalizedDescriptionKey: @"photos authorization timed out — no system prompt? rebuild with Info.plist embedded and run scripts/mac-sign.sh $(which nubilo)"
			}];
		}
		return 0;
	}
	if (!ok && outErr) {
		NSString *msg = [NSString stringWithFormat:
			@"photos access %@ — grant access in the system prompt, or System Settings → Privacy & Security → Photos",
			nubilo_pk_status_name(got)];
		*outErr = [NSError errorWithDomain:@"nubilo" code:1 userInfo:@{NSLocalizedDescriptionKey: msg}];
	}
	return ok ? 1 : 0;
}

static NSArray<NSString *> *nubilo_pk_json_ids(const char *json) {
	if (!json || json[0] == 0) {
		return @[];
	}
	NSData *data = [NSData dataWithBytes:json length:strlen(json)];
	id obj = [NSJSONSerialization JSONObjectWithData:data options:0 error:nil];
	if ([obj isKindOfClass:[NSArray class]]) {
		return obj;
	}
	return @[];
}

static BOOL nubilo_pk_is_media(PHAsset *a) {
	return a.mediaType == PHAssetMediaTypeImage || a.mediaType == PHAssetMediaTypeVideo;
}

static NSString *nubilo_pk_kind(PHAsset *a) {
	if (a.mediaType == PHAssetMediaTypeVideo) {
		return @"video";
	}
	if (a.mediaSubtypes & PHAssetMediaSubtypePhotoLive) {
		return @"live";
	}
	NSArray<PHAssetResource *> *resources = [PHAssetResource assetResourcesForAsset:a];
	for (PHAssetResource *r in resources) {
		if (r.type == PHAssetResourceTypePhoto || r.type == PHAssetResourceTypeFullSizePhoto ||
		    r.type == PHAssetResourceTypeAlternatePhoto) {
			NSString *uti = r.uniformTypeIdentifier ?: @"";
			if ([uti containsString:@"raw"] || [uti containsString:@"dng"] ||
			    [uti isEqualToString:@"public.camera-raw-image"] ||
			    [uti containsString:@"adobe.raw"]) {
				return @"raw";
			}
		}
		if (@available(macOS 13.0, *)) {
			if (r.type == PHAssetResourceTypeAdjustmentBasePhoto) {
				continue;
			}
		}
	}
	return @"image";
}

static NSString *nubilo_pk_filename(PHAsset *a) {
	NSString *fn = @"photo.jpg";
	id v = [a valueForKey:@"filename"];
	if ([v isKindOfClass:[NSString class]] && [v length] > 0) {
		fn = v;
	}
	return fn;
}

static NSDictionary *nubilo_pk_asset_row(PHAsset *a, NSArray<NSString *> *albums) {
	NSDate *taken = a.creationDate ?: [NSDate date];
	NSMutableDictionary *row = [@{
		@"id": a.localIdentifier ?: @"",
		@"filename": nubilo_pk_filename(a),
		@"taken": @([taken timeIntervalSince1970]),
		@"mod": @([a.modificationDate timeIntervalSince1970]),
		@"width": @(a.pixelWidth),
		@"height": @(a.pixelHeight),
		@"kind": nubilo_pk_kind(a),
		@"duration": @(a.duration)
	} mutableCopy];
	if (albums.count > 0) {
		row[@"albums"] = albums;
	}
	return row;
}

char *nubilo_pk_list_albums(char **err) {
	NSError *e = nil;
	if (!nubilo_pk_access(&e)) {
		if (err) {
			*err = nubilo_pk_dup(e.localizedDescription ?: @"photos access denied");
		}
		return NULL;
	}
	PHFetchResult<PHAssetCollection *> *res = [PHAssetCollection fetchAssetCollectionsWithType:PHAssetCollectionTypeAlbum subtype:PHAssetCollectionSubtypeAny options:nil];
	NSMutableArray *out = [NSMutableArray array];
	[res enumerateObjectsUsingBlock:^(PHAssetCollection *c, NSUInteger idx, BOOL *stop) {
		[out addObject:@{ @"id": c.localIdentifier ?: @"", @"title": c.localizedTitle ?: @"" }];
	}];
	NSData *data = [NSJSONSerialization dataWithJSONObject:out options:0 error:&e];
	if (!data) {
		if (err) {
			*err = nubilo_pk_dup(@"json");
		}
		return NULL;
	}
	return nubilo_pk_dup([[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding]);
}

char *nubilo_pk_list_assets(const char *source, const char *album_ids_json, double after, double before, char **err) {
	NSError *e = nil;
	if (!nubilo_pk_access(&e)) {
		if (err) {
			*err = nubilo_pk_dup(e.localizedDescription ?: @"photos access denied");
		}
		return NULL;
	}
	NSString *src = [NSString stringWithUTF8String:source ? source : "all"];
	NSMutableArray *rows = [NSMutableArray array];
	if ([src isEqualToString:@"albums"]) {
		NSArray<NSString *> *ids = nubilo_pk_json_ids(album_ids_json);
		NSMutableArray<PHAsset *> *acc = [NSMutableArray array];
		NSMutableSet<NSString *> *seen = [NSMutableSet set];
		for (NSString *aid in ids) {
			PHFetchResult<PHAssetCollection *> *cols = [PHAssetCollection fetchAssetCollectionsWithLocalIdentifiers:@[ aid ] options:nil];
			PHAssetCollection *col = cols.firstObject;
			if (!col) {
				continue;
			}
			PHFetchResult<PHAsset *> *inAlbum = [PHAsset fetchAssetsInAssetCollection:col options:nil];
			[inAlbum enumerateObjectsUsingBlock:^(PHAsset *a, NSUInteger idx, BOOL *stop) {
				if (!nubilo_pk_is_media(a)) {
					return;
				}
				if ([seen containsObject:a.localIdentifier]) {
					return;
				}
				[seen addObject:a.localIdentifier];
				[acc addObject:a];
			}];
		}
		for (PHAsset *a in acc) {
			NSDate *taken = a.creationDate ?: [NSDate date];
			double ts = [taken timeIntervalSince1970];
			if (after > 0 && ts < after) {
				continue;
			}
			if (before > 0 && ts > before) {
				continue;
			}
			[rows addObject:nubilo_pk_asset_row(a, ids)];
		}
	} else {
		PHFetchOptions *opts = [[PHFetchOptions alloc] init];
		opts.predicate = [NSPredicate predicateWithFormat:@"mediaType == %d OR mediaType == %d",
			(int)PHAssetMediaTypeImage, (int)PHAssetMediaTypeVideo];
		PHFetchResult<PHAsset *> *assets = [PHAsset fetchAssetsWithOptions:opts];
		[assets enumerateObjectsUsingBlock:^(PHAsset *a, NSUInteger idx, BOOL *stop) {
			NSDate *taken = a.creationDate ?: [NSDate date];
			double ts = [taken timeIntervalSince1970];
			if (after > 0 && ts < after) {
				return;
			}
			if (before > 0 && ts > before) {
				return;
			}
			[rows addObject:nubilo_pk_asset_row(a, @[])];
		}];
	}
	NSData *data = [NSJSONSerialization dataWithJSONObject:rows options:0 error:&e];
	if (!data) {
		if (err) {
			*err = nubilo_pk_dup(@"json");
		}
		return NULL;
	}
	return nubilo_pk_dup([[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding]);
}

static PHAssetResource *nubilo_pk_pick_primary(PHAsset *asset) {
	NSArray<PHAssetResource *> *resources = [PHAssetResource assetResourcesForAsset:asset];
	PHAssetResource *raw = nil;
	PHAssetResource *photo = nil;
	PHAssetResource *full = nil;
	PHAssetResource *video = nil;
	PHAssetResource *fullVideo = nil;
	for (PHAssetResource *r in resources) {
		switch (r.type) {
		case PHAssetResourceTypePhoto:
			photo = r;
			break;
		case PHAssetResourceTypeFullSizePhoto:
			full = r;
			break;
		case PHAssetResourceTypeVideo:
			video = r;
			break;
		case PHAssetResourceTypeFullSizeVideo:
			fullVideo = r;
			break;
		case PHAssetResourceTypeAlternatePhoto: {
			NSString *uti = r.uniformTypeIdentifier ?: @"";
			if ([uti containsString:@"raw"] || [uti containsString:@"dng"] ||
			    [uti isEqualToString:@"public.camera-raw-image"]) {
				raw = r;
			}
			break;
		}
		default:
			break;
		}
	}
	if (asset.mediaType == PHAssetMediaTypeVideo) {
		return video ?: fullVideo;
	}
	if (raw) {
		return raw;
	}
	return photo ?: full;
}

static PHAssetResource *nubilo_pk_pick_paired_video(PHAsset *asset) {
	for (PHAssetResource *r in [PHAssetResource assetResourcesForAsset:asset]) {
		if (r.type == PHAssetResourceTypePairedVideo) {
			return r;
		}
	}
	return nil;
}

static int nubilo_pk_write_resource(PHAssetResource *resource, NSString *dest, char **err, NSString *failTag) {
	NSURL *url = [NSURL fileURLWithPath:dest];
	[[NSFileManager defaultManager] removeItemAtURL:url error:nil];
	PHAssetResourceRequestOptions *opts = [[PHAssetResourceRequestOptions alloc] init];
	opts.networkAccessAllowed = YES;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	__block BOOL ok = NO;
	__block NSError *werr = nil;
	[[PHAssetResourceManager defaultManager] writeDataForAssetResource:resource toFile:url options:opts completionHandler:^(NSError *error) {
		werr = error;
		ok = (error == nil);
		dispatch_semaphore_signal(sem);
	}];
	if (!nubilo_pk_wait(sem, kNubiloPKExportTimeout)) {
		if (err) {
			*err = strdup([[NSString stringWithFormat:@"%@: timeout waiting for iCloud", failTag] UTF8String]);
		}
		return 0;
	}
	if (!ok) {
		if (err) {
			NSString *msg = werr.localizedDescription ?: @"export failed";
			NSString *low = msg.lowercaseString;
			BOOL icloud = [low containsString:@"icloud"] || [low containsString:@"cloud"] ||
			              [low containsString:@"network"] || [low containsString:@"download"];
			*err = nubilo_pk_dup([NSString stringWithFormat:@"%@: %@", icloud ? @"icloud_fetch" : failTag, msg]);
		}
		return 0;
	}
	return 1;
}

int nubilo_pk_export_original(const char *local_id, const char *dest_path, char **err) {
	NSError *e = nil;
	if (!nubilo_pk_access(&e)) {
		if (err) {
			*err = nubilo_pk_dup(e.localizedDescription ?: @"photos access denied");
		}
		return 0;
	}
	NSString *lid = [NSString stringWithUTF8String:local_id ? local_id : ""];
	NSString *dest = [NSString stringWithUTF8String:dest_path ? dest_path : ""];
	PHFetchResult<PHAsset *> *res = [PHAsset fetchAssetsWithLocalIdentifiers:@[ lid ] options:nil];
	PHAsset *asset = res.firstObject;
	if (!asset) {
		if (err) {
			*err = strdup("asset not found");
		}
		return 0;
	}
	PHAssetResource *resource = nubilo_pk_pick_primary(asset);
	if (!resource) {
		if (err) {
			*err = strdup("no original resource");
		}
		return 0;
	}
	return nubilo_pk_write_resource(resource, dest, err, @"export_failed");
}

int nubilo_pk_export_live_movie(const char *local_id, const char *dest_path, char **err) {
	NSError *e = nil;
	if (!nubilo_pk_access(&e)) {
		if (err) {
			*err = nubilo_pk_dup(e.localizedDescription ?: @"photos access denied");
		}
		return 0;
	}
	NSString *lid = [NSString stringWithUTF8String:local_id ? local_id : ""];
	NSString *dest = [NSString stringWithUTF8String:dest_path ? dest_path : ""];
	PHFetchResult<PHAsset *> *res = [PHAsset fetchAssetsWithLocalIdentifiers:@[ lid ] options:nil];
	PHAsset *asset = res.firstObject;
	if (!asset) {
		if (err) {
			*err = strdup("asset not found");
		}
		return 0;
	}
	PHAssetResource *resource = nubilo_pk_pick_paired_video(asset);
	if (!resource) {
		return 0;
	}
	return nubilo_pk_write_resource(resource, dest, err, @"export_failed");
}

int nubilo_pk_import(const char *src_path, const char *album_id, const char *filename, char **out_local_id, char **err) {
	NSError *e = nil;
	if (!nubilo_pk_access(&e)) {
		if (err) {
			*err = nubilo_pk_dup(e.localizedDescription ?: @"photos access denied");
		}
		return 0;
	}
	NSString *path = [NSString stringWithUTF8String:src_path ? src_path : ""];
	NSString *aid = [NSString stringWithUTF8String:album_id ? album_id : ""];
	NSURL *url = [NSURL fileURLWithPath:path];
	if (![[NSFileManager defaultManager] fileExistsAtPath:path]) {
		if (err) {
			*err = strdup("import source missing");
		}
		return 0;
	}
	NSString *ext = path.pathExtension.lowercaseString;
	BOOL isVideo = [@[ @"mp4", @"mov", @"m4v" ] containsObject:ext];

	__block NSString *newID = nil;
	__block NSError *werr = nil;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	[[PHPhotoLibrary sharedPhotoLibrary] performChanges:^{
		PHAssetChangeRequest *req = nil;
		if (isVideo) {
			req = [PHAssetChangeRequest creationRequestForAssetFromVideoAtFileURL:url];
		} else {
			req = [PHAssetChangeRequest creationRequestForAssetFromImageAtFileURL:url];
		}
		if (!req) {
			return;
		}
		PHObjectPlaceholder *ph = req.placeholderForCreatedAsset;
		newID = ph.localIdentifier;
		if (aid.length > 0) {
			PHFetchResult<PHAssetCollection *> *cols = [PHAssetCollection fetchAssetCollectionsWithLocalIdentifiers:@[ aid ] options:nil];
			PHAssetCollection *col = cols.firstObject;
			if (col) {
				PHAssetCollectionChangeRequest *creq = [PHAssetCollectionChangeRequest changeRequestForAssetCollection:col];
				[creq addAssets:@[ ph ]];
			}
		}
	} completionHandler:^(BOOL success, NSError *error) {
		werr = error;
		if (!success) {
			newID = nil;
		}
		dispatch_semaphore_signal(sem);
	}];
	if (!nubilo_pk_wait(sem, 60.0)) {
		if (err) {
			*err = strdup("import timeout");
		}
		return 0;
	}
	if (!newID.length) {
		if (err) {
			*err = nubilo_pk_dup(werr.localizedDescription ?: @"import failed");
		}
		return 0;
	}
	if (out_local_id) {
		*out_local_id = nubilo_pk_dup(newID);
	}
	(void)filename;
	return 1;
}
