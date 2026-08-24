//go:build darwin

#import "photokit_darwin.h"
#import <Photos/Photos.h>
#import <Foundation/Foundation.h>

static char *nubilo_pk_dup(NSString *s) {
	if (!s) {
		return strdup("");
	}
	const char *u = [s UTF8String];
	return strdup(u ? u : "");
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
	__block BOOL ok = NO;
	__block NSError *err = nil;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	if (@available(macOS 14.0, *)) {
		[PHPhotoLibrary requestAuthorizationForAccessLevel:PHAccessLevelReadWrite handler:^(PHAuthorizationStatus status) {
			ok = (status == PHAuthorizationStatusAuthorized || status == PHAuthorizationStatusLimited);
			dispatch_semaphore_signal(sem);
		}];
	} else {
		[PHPhotoLibrary requestAuthorization:^(PHAuthorizationStatus status) {
			ok = (status == PHAuthorizationStatusAuthorized);
			dispatch_semaphore_signal(sem);
		}];
	}
	while (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 50 * NSEC_PER_MSEC))) {
		[[NSRunLoop currentRunLoop] runMode:NSDefaultRunLoopMode beforeDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
	}
	if (outErr) {
		*outErr = err;
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
	PHFetchResult<PHAsset *> *assets = nil;
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
				if (a.mediaType != PHAssetMediaTypeImage) {
					return;
				}
				if ([seen containsObject:a.localIdentifier]) {
					return;
				}
				[seen addObject:a.localIdentifier];
				[acc addObject:a];
			}];
		}
		PHAsset *dummy = nil;
		(void)dummy;
		NSMutableArray *rows = [NSMutableArray array];
		for (PHAsset *a in acc) {
			NSDate *taken = a.creationDate ?: [NSDate date];
			double ts = [taken timeIntervalSince1970];
			if (after > 0 && ts < after) {
				continue;
			}
			if (before > 0 && ts > before) {
				continue;
			}
			NSMutableArray *albums = [NSMutableArray array];
			for (NSString *aid in ids) {
				[albums addObject:aid];
			}
			NSString *fn = @"photo.jpg";
			id v = [a valueForKey:@"filename"];
			if ([v isKindOfClass:[NSString class]] && [v length] > 0) {
				fn = v;
			}
			[rows addObject:@{
				@"id": a.localIdentifier ?: @"",
				@"filename": fn,
				@"taken": @(ts),
				@"mod": @([a.modificationDate timeIntervalSince1970]),
				@"width": @(a.pixelWidth),
				@"height": @(a.pixelHeight),
				@"albums": albums
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
	PHFetchOptions *opts = [[PHFetchOptions alloc] init];
	opts.predicate = [NSPredicate predicateWithFormat:@"mediaType == %d", PHAssetMediaTypeImage];
	assets = [PHAsset fetchAssetsWithOptions:opts];
	NSMutableArray *rows = [NSMutableArray array];
	[assets enumerateObjectsUsingBlock:^(PHAsset *a, NSUInteger idx, BOOL *stop) {
		NSDate *taken = a.creationDate ?: [NSDate date];
		double ts = [taken timeIntervalSince1970];
		if (after > 0 && ts < after) {
			return;
		}
		if (before > 0 && ts > before) {
			return;
		}
		NSString *fn = @"photo.jpg";
		if ([a respondsToSelector:@selector(valueForKey:)]) {
			id v = [a valueForKey:@"filename"];
			if ([v isKindOfClass:[NSString class]] && [v length] > 0) {
				fn = v;
			}
		}
		[rows addObject:@{
			@"id": a.localIdentifier ?: @"",
			@"filename": fn,
			@"taken": @(ts),
			@"mod": @([a.modificationDate timeIntervalSince1970]),
			@"width": @(a.pixelWidth),
			@"height": @(a.pixelHeight)
		}];
	}];
	NSData *data = [NSJSONSerialization dataWithJSONObject:rows options:0 error:&e];
	if (!data) {
		if (err) {
			*err = nubilo_pk_dup(@"json");
		}
		return NULL;
	}
	return nubilo_pk_dup([[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding]);
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
	PHAssetResource *resource = nil;
	for (PHAssetResource *r in [PHAssetResource assetResourcesForAsset:asset]) {
		if (r.type == PHAssetResourceTypePhoto || r.type == PHAssetResourceTypeFullSizePhoto) {
			resource = r;
			if (r.type == PHAssetResourceTypePhoto) {
				break;
			}
		}
	}
	if (!resource) {
		if (err) {
			*err = strdup("no original resource");
		}
		return 0;
	}
	NSURL *url = [NSURL fileURLWithPath:dest];
	[[NSFileManager defaultManager] removeItemAtURL:url error:nil];
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	__block BOOL ok = NO;
	__block NSError *werr = nil;
	[[PHAssetResourceManager defaultManager] writeDataForAssetResource:resource toFile:url options:nil completionHandler:^(NSError *error) {
		werr = error;
		ok = (error == nil);
		dispatch_semaphore_signal(sem);
	}];
	while (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 50 * NSEC_PER_MSEC))) {
		[[NSRunLoop currentRunLoop] runMode:NSDefaultRunLoopMode beforeDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
	}
	if (!ok) {
		if (err) {
			*err = nubilo_pk_dup(werr.localizedDescription ?: @"export failed");
		}
		return 0;
	}
	return 1;
}
