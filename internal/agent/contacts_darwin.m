//go:build darwin

#import "contacts_darwin.h"
#import <Contacts/Contacts.h>
#import <Foundation/Foundation.h>

static char *nubilo_cn_dup(NSString *s) {
	if (!s) {
		return strdup("");
	}
	const char *u = [s UTF8String];
	return strdup(u ? u : "");
}

static CNContactStore *nubilo_cn_store(void) {
	static CNContactStore *store;
	static dispatch_once_t once;
	dispatch_once(&once, ^{ store = [[CNContactStore alloc] init]; });
	return store;
}

static int nubilo_cn_access(NSError **outErr) {
	CNAuthorizationStatus st = [CNContactStore authorizationStatusForEntityType:CNEntityTypeContacts];
	if (st == CNAuthorizationStatusAuthorized) {
		return 1;
	}
	__block BOOL ok = NO;
	__block NSError *err = nil;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	[nubilo_cn_store() requestAccessForEntityType:CNEntityTypeContacts completionHandler:^(BOOL granted, NSError *e) {
		ok = granted;
		err = e;
		dispatch_semaphore_signal(sem);
	}];
	while (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 50 * NSEC_PER_MSEC))) {
		[[NSRunLoop currentRunLoop] runMode:NSDefaultRunLoopMode beforeDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
	}
	if (outErr) {
		*outErr = err;
	}
	return ok ? 1 : 0;
}

char *nubilo_cn_list(char **err) {
	NSError *e = nil;
	if (!nubilo_cn_access(&e)) {
		if (err) {
			*err = nubilo_cn_dup(e.localizedDescription ?: @"contacts access denied");
		}
		return NULL;
	}
	NSArray *keys = @[ CNContactIdentifierKey, CNContactGivenNameKey, CNContactFamilyNameKey, CNContactEmailAddressesKey ];
	CNContactFetchRequest *req = [[CNContactFetchRequest alloc] initWithKeysToFetch:keys];
	NSMutableArray *out = [NSMutableArray array];
	BOOL ok = [nubilo_cn_store() enumerateContactsWithFetchRequest:req error:&e usingBlock:^(CNContact *c, BOOL *stop) {
		NSString *email = @"";
		if (c.emailAddresses.count > 0) {
			email = c.emailAddresses[0].value;
		}
		NSString *fn = [NSString stringWithFormat:@"%@ %@", c.givenName ?: @"", c.familyName ?: @""];
		fn = [fn stringByTrimmingCharactersInSet:[NSCharacterSet whitespaceCharacterSet]];
		[out addObject:@{
			@"id": c.identifier ?: @"",
			@"uid": c.identifier ?: @"",
			@"given": c.givenName ?: @"",
			@"family": c.familyName ?: @"",
			@"fn": fn,
			@"email": email
		}];
	}];
	if (!ok) {
		if (err) {
			*err = nubilo_cn_dup(e.localizedDescription ?: @"enumerate failed");
		}
		return NULL;
	}
	NSData *data = [NSJSONSerialization dataWithJSONObject:out options:0 error:&e];
	if (!data) {
		if (err) {
			*err = nubilo_cn_dup(@"json");
		}
		return NULL;
	}
	return nubilo_cn_dup([[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding]);
}

char *nubilo_cn_save(const char *contact_id, const char *uid, const char *given, const char *family, const char *fn, const char *email, char **err) {
	NSError *e = nil;
	if (!nubilo_cn_access(&e)) {
		if (err) {
			*err = nubilo_cn_dup(e.localizedDescription ?: @"contacts access denied");
		}
		return NULL;
	}
	CNContactStore *store = nubilo_cn_store();
	NSString *cid = [NSString stringWithUTF8String:contact_id ? contact_id : ""];
	CNMutableContact *c = nil;
	if (cid.length > 0) {
		CNContact *existing = [store unifiedContactWithIdentifier:cid keysToFetch:@[ CNContactGivenNameKey, CNContactFamilyNameKey, CNContactEmailAddressesKey ] error:&e];
		if (existing) {
			c = [existing mutableCopy];
		}
	}
	if (!c) {
		c = [[CNMutableContact alloc] init];
	}
	c.givenName = [NSString stringWithUTF8String:given ? given : ""];
	c.familyName = [NSString stringWithUTF8String:family ? family : ""];
	NSString *em = [NSString stringWithUTF8String:email ? email : ""];
	if (em.length > 0) {
		c.emailAddresses = @[ [CNLabeledValue labeledValueWithLabel:CNLabelHome value:em] ];
	}
	CNSaveRequest *req = [[CNSaveRequest alloc] init];
	if (cid.length > 0 && c.identifier.length > 0) {
		[req updateContact:c];
	} else {
		[req addContact:c toContainerWithIdentifier:nil];
	}
	if (![store executeSaveRequest:req error:&e]) {
		if (err) {
			*err = nubilo_cn_dup(e.localizedDescription ?: @"save failed");
		}
		return NULL;
	}
	(void)uid;
	(void)fn;
	return nubilo_cn_dup(c.identifier);
}

int nubilo_cn_delete(const char *contact_id, char **err) {
	NSError *e = nil;
	if (!nubilo_cn_access(&e)) {
		if (err) {
			*err = nubilo_cn_dup(e.localizedDescription ?: @"contacts access denied");
		}
		return 0;
	}
	NSString *cid = [NSString stringWithUTF8String:contact_id ? contact_id : ""];
	CNContact *existing = [nubilo_cn_store() unifiedContactWithIdentifier:cid keysToFetch:@[ CNContactIdentifierKey ] error:&e];
	if (!existing) {
		return 1;
	}
	CNSaveRequest *req = [[CNSaveRequest alloc] init];
	[req deleteContact:[existing mutableCopy]];
	if (![nubilo_cn_store() executeSaveRequest:req error:&e]) {
		if (err) {
			*err = nubilo_cn_dup(e.localizedDescription ?: @"delete failed");
		}
		return 0;
	}
	return 1;
}
