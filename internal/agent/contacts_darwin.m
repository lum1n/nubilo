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

static NSString *nubilo_cn_label_type(NSString *label) {
	if (!label) {
		return @"other";
	}
	if ([label isEqualToString:CNLabelHome]) {
		return @"home";
	}
	if ([label isEqualToString:CNLabelWork]) {
		return @"work";
	}
	if ([label isEqualToString:CNLabelOther]) {
		return @"other";
	}
	if ([label isEqualToString:CNLabelPhoneNumberMobile] || [label isEqualToString:CNLabelPhoneNumberiPhone]) {
		return @"cell";
	}
	if ([label isEqualToString:CNLabelPhoneNumberMain]) {
		return @"main";
	}
	if ([label isEqualToString:CNLabelPhoneNumberHomeFax] || [label isEqualToString:CNLabelPhoneNumberWorkFax] || [label isEqualToString:CNLabelPhoneNumberOtherFax]) {
		return @"fax";
	}
	if ([label isEqualToString:CNLabelPhoneNumberPager]) {
		return @"pager";
	}
	return @"other";
}

static NSString *nubilo_cn_type_label(NSString *type, BOOL phone) {
	NSString *t = [(type ?: @"") lowercaseString];
	if ([t isEqualToString:@"home"]) {
		return CNLabelHome;
	}
	if ([t isEqualToString:@"work"]) {
		return CNLabelWork;
	}
	if (phone) {
		if ([t isEqualToString:@"cell"] || [t isEqualToString:@"mobile"]) {
			return CNLabelPhoneNumberMobile;
		}
		if ([t isEqualToString:@"main"]) {
			return CNLabelPhoneNumberMain;
		}
		if ([t isEqualToString:@"fax"]) {
			return CNLabelPhoneNumberHomeFax;
		}
		if ([t isEqualToString:@"pager"]) {
			return CNLabelPhoneNumberPager;
		}
	}
	return CNLabelOther;
}

static NSDictionary *nubilo_cn_addr_dict(CNPostalAddress *a, NSString *label) {
	return @{
		@"label": nubilo_cn_label_type(label) ?: @"other",
		@"street": a.street ?: @"",
		@"city": a.city ?: @"",
		@"region": a.state ?: @"",
		@"postal": a.postalCode ?: @"",
		@"country": a.country ?: @""
	};
}

char *nubilo_cn_list(char **err) {
	NSError *e = nil;
	if (!nubilo_cn_access(&e)) {
		if (err) {
			*err = nubilo_cn_dup(e.localizedDescription ?: @"contacts access denied");
		}
		return NULL;
	}
	NSArray *keys = @[
		CNContactIdentifierKey,
		CNContactGivenNameKey,
		CNContactFamilyNameKey,
		CNContactOrganizationNameKey,
		CNContactNicknameKey,
		CNContactNoteKey,
		CNContactEmailAddressesKey,
		CNContactPhoneNumbersKey,
		CNContactPostalAddressesKey,
		CNContactUrlAddressesKey,
		CNContactBirthdayKey,
		CNContactImageDataKey
	];
	CNContactFetchRequest *req = [[CNContactFetchRequest alloc] initWithKeysToFetch:keys];
	NSMutableArray *out = [NSMutableArray array];
	BOOL ok = [nubilo_cn_store() enumerateContactsWithFetchRequest:req error:&e usingBlock:^(CNContact *c, BOOL *stop) {
		NSMutableArray *emails = [NSMutableArray array];
		for (CNLabeledValue *lv in c.emailAddresses) {
			NSString *v = (NSString *)lv.value;
			if (v.length == 0) {
				continue;
			}
			[emails addObject:@{ @"label": nubilo_cn_label_type(lv.label), @"value": v }];
		}
		NSMutableArray *phones = [NSMutableArray array];
		for (CNLabeledValue *lv in c.phoneNumbers) {
			CNPhoneNumber *pn = (CNPhoneNumber *)lv.value;
			NSString *v = pn.stringValue ?: @"";
			if (v.length == 0) {
				continue;
			}
			[phones addObject:@{ @"label": nubilo_cn_label_type(lv.label), @"value": v }];
		}
		NSMutableArray *addrs = [NSMutableArray array];
		for (CNLabeledValue *lv in c.postalAddresses) {
			CNPostalAddress *pa = (CNPostalAddress *)lv.value;
			[addrs addObject:nubilo_cn_addr_dict(pa, lv.label)];
		}
		NSMutableArray *urls = [NSMutableArray array];
		for (CNLabeledValue *lv in c.urlAddresses) {
			NSString *v = (NSString *)lv.value;
			if (v.length == 0) {
				continue;
			}
			[urls addObject:@{ @"label": nubilo_cn_label_type(lv.label), @"value": v }];
		}
		NSString *fn = [NSString stringWithFormat:@"%@ %@", c.givenName ?: @"", c.familyName ?: @""];
		fn = [fn stringByTrimmingCharactersInSet:[NSCharacterSet whitespaceCharacterSet]];
		if (fn.length == 0 && c.organizationName.length > 0) {
			fn = c.organizationName;
		}
		if (fn.length == 0 && c.nickname.length > 0) {
			fn = c.nickname;
		}
		NSString *bday = @"";
		NSDateComponents *bd = c.birthday;
		if (bd && bd.month != NSDateComponentUndefined && bd.day != NSDateComponentUndefined) {
			if (bd.year != NSDateComponentUndefined && bd.year > 0) {
				bday = [NSString stringWithFormat:@"%04ld-%02ld-%02ld", (long)bd.year, (long)bd.month, (long)bd.day];
			} else {
				bday = [NSString stringWithFormat:@"--%02ld-%02ld", (long)bd.month, (long)bd.day];
			}
		}
		NSString *photoB64 = @"";
		if (c.imageData.length > 0) {
			photoB64 = [c.imageData base64EncodedStringWithOptions:0];
		}
		[out addObject:@{
			@"id": c.identifier ?: @"",
			@"uid": c.identifier ?: @"",
			@"given": c.givenName ?: @"",
			@"family": c.familyName ?: @"",
			@"fn": fn,
			@"org": c.organizationName ?: @"",
			@"nickname": c.nickname ?: @"",
			@"note": c.note ?: @"",
			@"emails": emails,
			@"phones": phones,
			@"addresses": addrs,
			@"urls": urls,
			@"birthday": bday,
			@"photo_b64": photoB64
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

char *nubilo_cn_save(const char *contact_id, const char *payload_json, char **err) {
	NSError *e = nil;
	if (!nubilo_cn_access(&e)) {
		if (err) {
			*err = nubilo_cn_dup(e.localizedDescription ?: @"contacts access denied");
		}
		return NULL;
	}
	NSData *raw = [NSData dataWithBytes:payload_json ? payload_json : "{}" length:payload_json ? strlen(payload_json) : 2];
	NSDictionary *payload = [NSJSONSerialization JSONObjectWithData:raw options:0 error:&e];
	if (![payload isKindOfClass:[NSDictionary class]]) {
		if (err) {
			*err = nubilo_cn_dup(@"bad contact payload");
		}
		return NULL;
	}
	CNContactStore *store = nubilo_cn_store();
	NSString *cid = [NSString stringWithUTF8String:contact_id ? contact_id : ""];
	NSArray *keys = @[
		CNContactGivenNameKey,
		CNContactFamilyNameKey,
		CNContactOrganizationNameKey,
		CNContactNicknameKey,
		CNContactNoteKey,
		CNContactEmailAddressesKey,
		CNContactPhoneNumbersKey,
		CNContactPostalAddressesKey,
		CNContactUrlAddressesKey,
		CNContactBirthdayKey,
		CNContactImageDataKey
	];
	CNMutableContact *c = nil;
	if (cid.length > 0) {
		CNContact *existing = [store unifiedContactWithIdentifier:cid keysToFetch:keys error:&e];
		if (existing) {
			c = [existing mutableCopy];
		}
	}
	if (!c) {
		c = [[CNMutableContact alloc] init];
	}
	NSString *given = payload[@"given"];
	NSString *family = payload[@"family"];
	NSString *fn = payload[@"fn"];
	c.givenName = [given isKindOfClass:[NSString class]] ? given : @"";
	c.familyName = [family isKindOfClass:[NSString class]] ? family : @"";
	if (c.givenName.length == 0 && c.familyName.length == 0 && [fn isKindOfClass:[NSString class]] && fn.length > 0) {
		c.givenName = fn;
	}
	NSString *org = payload[@"org"];
	c.organizationName = [org isKindOfClass:[NSString class]] ? org : @"";
	NSString *nick = payload[@"nickname"];
	c.nickname = [nick isKindOfClass:[NSString class]] ? nick : @"";
	NSString *note = payload[@"note"];
	c.note = [note isKindOfClass:[NSString class]] ? note : @"";

	NSMutableArray *emails = [NSMutableArray array];
	id emailList = payload[@"emails"];
	if ([emailList isKindOfClass:[NSArray class]]) {
		for (id item in emailList) {
			if (![item isKindOfClass:[NSDictionary class]]) {
				continue;
			}
			NSString *v = item[@"value"];
			if (![v isKindOfClass:[NSString class]] || v.length == 0) {
				continue;
			}
			NSString *label = nubilo_cn_type_label(item[@"label"], NO);
			[emails addObject:[CNLabeledValue labeledValueWithLabel:label value:v]];
		}
	}
	c.emailAddresses = emails;

	NSMutableArray *phones = [NSMutableArray array];
	id phoneList = payload[@"phones"];
	if ([phoneList isKindOfClass:[NSArray class]]) {
		for (id item in phoneList) {
			if (![item isKindOfClass:[NSDictionary class]]) {
				continue;
			}
			NSString *v = item[@"value"];
			if (![v isKindOfClass:[NSString class]] || v.length == 0) {
				continue;
			}
			NSString *label = nubilo_cn_type_label(item[@"label"], YES);
			[phones addObject:[CNLabeledValue labeledValueWithLabel:label value:[CNPhoneNumber phoneNumberWithStringValue:v]]];
		}
	}
	c.phoneNumbers = phones;

	NSMutableArray *addrs = [NSMutableArray array];
	id addrList = payload[@"addresses"];
	if ([addrList isKindOfClass:[NSArray class]]) {
		for (id item in addrList) {
			if (![item isKindOfClass:[NSDictionary class]]) {
				continue;
			}
			CNMutablePostalAddress *pa = [[CNMutablePostalAddress alloc] init];
			NSString *street = item[@"street"];
			NSString *city = item[@"city"];
			NSString *region = item[@"region"];
			NSString *postal = item[@"postal"];
			NSString *country = item[@"country"];
			pa.street = [street isKindOfClass:[NSString class]] ? street : @"";
			pa.city = [city isKindOfClass:[NSString class]] ? city : @"";
			pa.state = [region isKindOfClass:[NSString class]] ? region : @"";
			pa.postalCode = [postal isKindOfClass:[NSString class]] ? postal : @"";
			pa.country = [country isKindOfClass:[NSString class]] ? country : @"";
			if (pa.street.length == 0 && pa.city.length == 0 && pa.state.length == 0 && pa.postalCode.length == 0 && pa.country.length == 0) {
				continue;
			}
			NSString *label = nubilo_cn_type_label(item[@"label"], NO);
			[addrs addObject:[CNLabeledValue labeledValueWithLabel:label value:pa]];
		}
	}
	c.postalAddresses = addrs;

	NSMutableArray *urls = [NSMutableArray array];
	id urlList = payload[@"urls"];
	if ([urlList isKindOfClass:[NSArray class]]) {
		for (id item in urlList) {
			if (![item isKindOfClass:[NSDictionary class]]) {
				continue;
			}
			NSString *v = item[@"value"];
			if (![v isKindOfClass:[NSString class]] || v.length == 0) {
				continue;
			}
			NSString *label = nubilo_cn_type_label(item[@"label"], NO);
			[urls addObject:[CNLabeledValue labeledValueWithLabel:label value:v]];
		}
	}
	c.urlAddresses = urls;

	NSString *bday = payload[@"birthday"];
	if ([bday isKindOfClass:[NSString class]] && bday.length > 0) {
		NSDateComponents *dc = [[NSDateComponents alloc] init];
		dc.calendar = [NSCalendar calendarWithIdentifier:NSCalendarIdentifierGregorian];
		if ([bday hasPrefix:@"--"] && bday.length >= 7) {
			// --MM-DD
			dc.month = [[bday substringWithRange:NSMakeRange(2, 2)] integerValue];
			dc.day = [[bday substringWithRange:NSMakeRange(5, 2)] integerValue];
			dc.year = NSDateComponentUndefined;
		} else if (bday.length >= 10) {
			dc.year = [[bday substringWithRange:NSMakeRange(0, 4)] integerValue];
			dc.month = [[bday substringWithRange:NSMakeRange(5, 2)] integerValue];
			dc.day = [[bday substringWithRange:NSMakeRange(8, 2)] integerValue];
		}
		if (dc.month != 0 && dc.day != 0) {
			c.birthday = dc;
		} else {
			c.birthday = nil;
		}
	} else {
		c.birthday = nil;
	}

	NSString *photoB64 = payload[@"photo_b64"];
	if ([photoB64 isKindOfClass:[NSString class]] && photoB64.length > 0) {
		NSData *img = [[NSData alloc] initWithBase64EncodedString:photoB64 options:NSDataBase64DecodingIgnoreUnknownCharacters];
		c.imageData = img.length > 0 ? img : nil;
	} else {
		c.imageData = nil;
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

void nubilo_cn_watch_changes(void (*cb)(void)) {
	static dispatch_once_t once;
	static void (*callback)(void);
	callback = cb;
	dispatch_once(&once, ^{
		[[NSNotificationCenter defaultCenter] addObserverForName:CNContactStoreDidChangeNotification
			object:nil queue:nil usingBlock:^(NSNotification *note) {
				(void)note;
				if (callback) {
					callback();
				}
			}];
	});
}
