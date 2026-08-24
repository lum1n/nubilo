//go:build darwin

#import "eventkit_darwin.h"
#import <EventKit/EventKit.h>
#import <Foundation/Foundation.h>

static EKEventStore *nubilo_ek_store(void) {
	static EKEventStore *store;
	static dispatch_once_t once;
	dispatch_once(&once, ^{ store = [[EKEventStore alloc] init]; });
	return store;
}

static char *nubilo_dup(NSString *s) {
	if (!s) {
		return strdup("");
	}
	const char *u = [s UTF8String];
	return strdup(u ? u : "");
}

static int nubilo_ek_access(NSError **outErr) {
	EKEventStore *store = nubilo_ek_store();
	EKAuthorizationStatus st = [EKEventStore authorizationStatusForEntityType:EKEntityTypeEvent];
	if (st == EKAuthorizationStatusAuthorized) {
		return 1;
	}
	__block BOOL ok = NO;
	__block NSError *err = nil;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	[store requestAccessToEntityType:EKEntityTypeEvent completion:^(BOOL granted, NSError *e) {
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

char *nubilo_ek_list_calendars(char **err) {
	NSError *e = nil;
	if (!nubilo_ek_access(&e)) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"calendar access denied");
		}
		return NULL;
	}
	NSMutableArray *out = [NSMutableArray array];
	for (EKCalendar *cal in [nubilo_ek_store() calendarsForEntityType:EKEntityTypeEvent]) {
		if (cal.calendarIdentifier.length == 0) {
			continue;
		}
		[out addObject:@{ @"id": cal.calendarIdentifier, @"title": cal.title ?: @"" }];
	}
	NSData *data = [NSJSONSerialization dataWithJSONObject:out options:0 error:&e];
	if (!data) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"json");
		}
		return NULL;
	}
	return nubilo_dup([[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding]);
}

char *nubilo_ek_list_events(const char *calendar_id, double start, double end, char **err) {
	NSError *e = nil;
	if (!nubilo_ek_access(&e)) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"calendar access denied");
		}
		return NULL;
	}
	EKEventStore *store = nubilo_ek_store();
	EKCalendar *want = nil;
	NSString *cid = [NSString stringWithUTF8String:calendar_id ? calendar_id : ""];
	for (EKCalendar *cal in [store calendarsForEntityType:EKEntityTypeEvent]) {
		if ([cal.calendarIdentifier isEqualToString:cid]) {
			want = cal;
			break;
		}
	}
	if (!want) {
		if (err) {
			*err = strdup("calendar not found");
		}
		return NULL;
	}
	NSDate *from = [NSDate dateWithTimeIntervalSince1970:start];
	NSDate *to = [NSDate dateWithTimeIntervalSince1970:end];
	NSPredicate *pred = [store predicateForEventsWithStartDate:from endDate:to calendars:@[ want ]];
	NSMutableArray *out = [NSMutableArray array];
	for (EKEvent *ev in [store eventsMatchingPredicate:pred]) {
		NSMutableDictionary *d = [NSMutableDictionary dictionary];
		d[@"id"] = ev.calendarItemIdentifier ?: @"";
		d[@"calendar_id"] = cid;
		d[@"uid"] = ev.calendarItemExternalIdentifier ?: ev.calendarItemIdentifier ?: @"";
		d[@"title"] = ev.title ?: @"";
		d[@"notes"] = ev.notes ?: @"";
		d[@"location"] = ev.location ?: @"";
		d[@"start"] = @([ev.startDate timeIntervalSince1970]);
		d[@"end"] = @([ev.endDate timeIntervalSince1970]);
		d[@"all_day"] = @(ev.allDay ? 1 : 0);
		[out addObject:d];
	}
	NSData *data = [NSJSONSerialization dataWithJSONObject:out options:0 error:&e];
	if (!data) {
		if (err) {
			*err = nubilo_dup(@"json");
		}
		return NULL;
	}
	return nubilo_dup([[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding]);
}

char *nubilo_ek_save_event(const char *calendar_id, const char *item_id, const char *uid, const char *title, const char *notes, const char *location, double start, double end, int all_day, char **err) {
	NSError *e = nil;
	if (!nubilo_ek_access(&e)) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"calendar access denied");
		}
		return NULL;
	}
	EKEventStore *store = nubilo_ek_store();
	NSString *cid = [NSString stringWithUTF8String:calendar_id ? calendar_id : ""];
	EKCalendar *cal = nil;
	for (EKCalendar *c in [store calendarsForEntityType:EKEntityTypeEvent]) {
		if ([c.calendarIdentifier isEqualToString:cid]) {
			cal = c;
			break;
		}
	}
	if (!cal) {
		if (err) {
			*err = strdup("calendar not found");
		}
		return NULL;
	}
	EKEvent *ev = nil;
	NSString *iid = [NSString stringWithUTF8String:item_id ? item_id : ""];
	if (iid.length > 0) {
		EKCalendarItem *item = [store calendarItemWithIdentifier:iid];
		if ([item isKindOfClass:[EKEvent class]]) {
			ev = (EKEvent *)item;
		}
	}
	if (!ev) {
		ev = [EKEvent eventWithEventStore:store];
	}
	ev.calendar = cal;
	ev.title = [NSString stringWithUTF8String:title ? title : ""];
	ev.notes = [NSString stringWithUTF8String:notes ? notes : ""];
	ev.location = [NSString stringWithUTF8String:location ? location : ""];
	ev.startDate = [NSDate dateWithTimeIntervalSince1970:start];
	ev.endDate = [NSDate dateWithTimeIntervalSince1970:end > start ? end : start + 3600];
	ev.allDay = all_day ? YES : NO;
	if (![store saveEvent:ev span:EKSpanThisEvent commit:YES error:&e]) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"save failed");
		}
		return NULL;
	}
	(void)uid;
	return nubilo_dup(ev.calendarItemIdentifier);
}

int nubilo_ek_delete_event(const char *item_id, char **err) {
	NSError *e = nil;
	if (!nubilo_ek_access(&e)) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"calendar access denied");
		}
		return 0;
	}
	NSString *iid = [NSString stringWithUTF8String:item_id ? item_id : ""];
	EKCalendarItem *item = [nubilo_ek_store() calendarItemWithIdentifier:iid];
	if (![item isKindOfClass:[EKEvent class]]) {
		return 1;
	}
	if (![nubilo_ek_store() removeEvent:(EKEvent *)item span:EKSpanThisEvent commit:YES error:&e]) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"delete failed");
		}
		return 0;
	}
	return 1;
}
