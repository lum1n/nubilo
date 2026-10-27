//go:build darwin && cgo

#import "eventkit_darwin.h"
#import <EventKit/EventKit.h>
#import <Foundation/Foundation.h>
#import <CoreGraphics/CoreGraphics.h>
#import <math.h>
#import <string.h>

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
	if (@available(macOS 14.0, *)) {
		if (st == EKAuthorizationStatusFullAccess || st == EKAuthorizationStatusWriteOnly) {
			return 1;
		}
	} else {
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
		if (st == EKAuthorizationStatusAuthorized) {
			return 1;
		}
#pragma clang diagnostic pop
	}
	__block BOOL ok = NO;
	__block NSError *err = nil;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	if (@available(macOS 14.0, *)) {
		[store requestFullAccessToEventsWithCompletion:^(BOOL granted, NSError *e) {
			ok = granted;
			err = e;
			dispatch_semaphore_signal(sem);
		}];
	} else {
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
		[store requestAccessToEntityType:EKEntityTypeEvent completion:^(BOOL granted, NSError *e) {
			ok = granted;
			err = e;
			dispatch_semaphore_signal(sem);
		}];
#pragma clang diagnostic pop
	}
	while (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 50 * NSEC_PER_MSEC))) {
		[[NSRunLoop currentRunLoop] runMode:NSDefaultRunLoopMode beforeDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
	}
	if (outErr) {
		*outErr = err;
	}
	return ok ? 1 : 0;
}

static NSString *nubilo_weekday(NSInteger d) {
	switch (d) {
	case 1:
		return @"SU";
	case 2:
		return @"MO";
	case 3:
		return @"TU";
	case 4:
		return @"WE";
	case 5:
		return @"TH";
	case 6:
		return @"FR";
	case 7:
		return @"SA";
	default:
		return @"MO";
	}
}

static NSString *nubilo_join_nums(NSArray<NSNumber *> *arr) {
	NSMutableArray *s = [NSMutableArray array];
	for (NSNumber *n in arr) {
		[s addObject:n.stringValue];
	}
	return [s componentsJoinedByString:@","];
}

static NSString *nubilo_ek_rrule(EKRecurrenceRule *rule, BOOL allDay) {
	if (!rule) {
		return @"";
	}
	NSMutableArray *p = [NSMutableArray array];
	switch (rule.frequency) {
	case EKRecurrenceFrequencyDaily:
		[p addObject:@"FREQ=DAILY"];
		break;
	case EKRecurrenceFrequencyWeekly:
		[p addObject:@"FREQ=WEEKLY"];
		break;
	case EKRecurrenceFrequencyMonthly:
		[p addObject:@"FREQ=MONTHLY"];
		break;
	case EKRecurrenceFrequencyYearly:
		[p addObject:@"FREQ=YEARLY"];
		break;
	default:
		return @"";
	}
	if (rule.interval > 1) {
		[p addObject:[NSString stringWithFormat:@"INTERVAL=%ld", (long)rule.interval]];
	}
	if (rule.firstDayOfTheWeek > 0) {
		[p addObject:[NSString stringWithFormat:@"WKST=%@", nubilo_weekday(rule.firstDayOfTheWeek)]];
	}
	if (rule.daysOfTheWeek.count) {
		NSMutableArray *days = [NSMutableArray array];
		for (EKRecurrenceDayOfWeek *d in rule.daysOfTheWeek) {
			NSString *w = nubilo_weekday(d.dayOfTheWeek);
			if (d.weekNumber != 0) {
				[days addObject:[NSString stringWithFormat:@"%ld%@", (long)d.weekNumber, w]];
			} else {
				[days addObject:w];
			}
		}
		[p addObject:[NSString stringWithFormat:@"BYDAY=%@", [days componentsJoinedByString:@","]]];
	}
	if (rule.daysOfTheMonth.count) {
		[p addObject:[NSString stringWithFormat:@"BYMONTHDAY=%@", nubilo_join_nums(rule.daysOfTheMonth)]];
	}
	if (rule.monthsOfTheYear.count) {
		[p addObject:[NSString stringWithFormat:@"BYMONTH=%@", nubilo_join_nums(rule.monthsOfTheYear)]];
	}
	if (rule.daysOfTheYear.count) {
		[p addObject:[NSString stringWithFormat:@"BYYEARDAY=%@", nubilo_join_nums(rule.daysOfTheYear)]];
	}
	if (rule.weeksOfTheYear.count) {
		[p addObject:[NSString stringWithFormat:@"BYWEEKNO=%@", nubilo_join_nums(rule.weeksOfTheYear)]];
	}
	if (rule.setPositions.count) {
		[p addObject:[NSString stringWithFormat:@"BYSETPOS=%@", nubilo_join_nums(rule.setPositions)]];
	}
	EKRecurrenceEnd *end = rule.recurrenceEnd;
	if (end.occurrenceCount > 0) {
		[p addObject:[NSString stringWithFormat:@"COUNT=%ld", (long)end.occurrenceCount]];
	} else if (end.endDate) {
		NSDateFormatter *fmt = [[NSDateFormatter alloc] init];
		fmt.locale = [NSLocale localeWithLocaleIdentifier:@"en_US_POSIX"];
		fmt.timeZone = [NSTimeZone timeZoneWithAbbreviation:@"UTC"];
		if (allDay) {
			fmt.dateFormat = @"yyyyMMdd";
		} else {
			fmt.dateFormat = @"yyyyMMdd'T'HHmmss'Z'";
		}
		[p addObject:[NSString stringWithFormat:@"UNTIL=%@", [fmt stringFromDate:end.endDate]]];
	}
	return [p componentsJoinedByString:@";"];
}

static NSString *nubilo_ek_series_id(NSString *eventID) {
	NSRange slash = [eventID rangeOfString:@"/"];
	if (slash.location == NSNotFound || slash.location == 0) {
		return eventID;
	}
	return [eventID substringToIndex:slash.location];
}

static NSString *nubilo_ek_tzname(EKEvent *ev) {
	NSTimeZone *tz = ev.timeZone ?: NSTimeZone.localTimeZone;
	return tz.name ?: @"";
}

static NSString *nubilo_ek_status(EKEvent *ev) {
	switch (ev.status) {
	case EKEventStatusTentative:
		return @"TENTATIVE";
	case EKEventStatusCanceled:
		return @"CANCELLED";
	case EKEventStatusConfirmed:
		return @"CONFIRMED";
	default:
		return @"";
	}
}

static NSString *nubilo_ek_transp(EKEvent *ev) {
	switch (ev.availability) {
	case EKEventAvailabilityFree:
		return @"TRANSPARENT";
	case EKEventAvailabilityBusy:
	case EKEventAvailabilityTentative:
	case EKEventAvailabilityUnavailable:
		return @"OPAQUE";
	default:
		return @"";
	}
}

static NSDictionary *nubilo_ek_person(EKParticipant *p) {
	if (!p) {
		return @{};
	}
	NSString *email = p.URL.absoluteString ?: @"";
	NSString *part = @"NEEDS-ACTION";
	switch (p.participantStatus) {
	case EKParticipantStatusAccepted:
		part = @"ACCEPTED";
		break;
	case EKParticipantStatusDeclined:
		part = @"DECLINED";
		break;
	case EKParticipantStatusTentative:
		part = @"TENTATIVE";
		break;
	case EKParticipantStatusDelegated:
		part = @"DELEGATED";
		break;
	case EKParticipantStatusCompleted:
		part = @"COMPLETED";
		break;
	case EKParticipantStatusInProcess:
		part = @"IN-PROCESS";
		break;
	default:
		break;
	}
	NSString *role = @"REQ-PARTICIPANT";
	switch (p.participantRole) {
	case EKParticipantRoleOptional:
		role = @"OPT-PARTICIPANT";
		break;
	case EKParticipantRoleChair:
		role = @"CHAIR";
		break;
	case EKParticipantRoleNonParticipant:
		role = @"NON-PARTICIPANT";
		break;
	default:
		break;
	}
	return @{
		@"name": p.name ?: @"",
		@"email": email,
		@"partstat": part,
		@"role": role
	};
}

static void nubilo_ek_fill_ics(NSMutableDictionary *d, EKEvent *ev) {
	d[@"url"] = ev.URL.absoluteString ?: @"";
	d[@"status"] = nubilo_ek_status(ev);
	d[@"transp"] = nubilo_ek_transp(ev);
	if (ev.organizer) {
		d[@"organizer"] = nubilo_ek_person(ev.organizer);
	}
	NSMutableArray *atts = [NSMutableArray array];
	for (EKParticipant *p in ev.attendees) {
		[atts addObject:nubilo_ek_person(p)];
	}
	if (atts.count) {
		d[@"attendees"] = atts;
	}
	NSMutableArray *alarms = [NSMutableArray array];
	for (EKAlarm *a in ev.alarms) {
		NSMutableDictionary *row = [NSMutableDictionary dictionary];
		if (a.absoluteDate) {
			row[@"abs"] = @([a.absoluteDate timeIntervalSince1970]);
		} else {
			row[@"offset"] = @(a.relativeOffset);
		}
		if (a.emailAddress.length) {
			row[@"action"] = @"EMAIL";
			row[@"email"] = a.emailAddress;
			row[@"desc"] = a.emailAddress;
		} else {
			row[@"action"] = @"DISPLAY";
			row[@"desc"] = @"Reminder";
		}
		[alarms addObject:row];
	}
	if (alarms.count) {
		d[@"alarms"] = alarms;
	}
}

static NSString *nubilo_cal_hex(EKCalendar *cal) {
	CGColorRef cg = cal.CGColor;
	if (!cg) {
		return @"";
	}
	size_t n = CGColorGetNumberOfComponents(cg);
	const CGFloat *c = CGColorGetComponents(cg);
	if (!c || n < 2) {
		return @"";
	}
	CGFloat r = 0, g = 0, b = 0, a = 1;
	if (n == 2) {
		r = g = b = c[0];
		a = c[1];
	} else {
		r = c[0];
		g = c[1];
		b = c[2];
		if (n >= 4) {
			a = c[3];
		}
	}
	int ri = (int)lround(fmin(1.0, fmax(0.0, r)) * 255.0);
	int gi = (int)lround(fmin(1.0, fmax(0.0, g)) * 255.0);
	int bi = (int)lround(fmin(1.0, fmax(0.0, b)) * 255.0);
	if (a >= 0.995) {
		return [NSString stringWithFormat:@"#%02X%02X%02X", ri, gi, bi];
	}
	int ai = (int)lround(fmin(1.0, fmax(0.0, a)) * 255.0);
	return [NSString stringWithFormat:@"#%02X%02X%02X%02X", ri, gi, bi, ai];
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
		NSMutableDictionary *row = [NSMutableDictionary dictionary];
		row[@"id"] = cal.calendarIdentifier;
		row[@"title"] = cal.title ?: @"";
		NSString *hex = nubilo_cal_hex(cal);
		if (hex.length > 0) {
			row[@"color"] = hex;
		}
		[out addObject:row];
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
	NSMutableDictionary *masters = [NSMutableDictionary dictionary];
	for (EKEvent *ev in [store eventsMatchingPredicate:pred]) {
		NSMutableDictionary *d = [NSMutableDictionary dictionary];
		d[@"id"] = ev.calendarItemIdentifier ?: @"";
		d[@"event_id"] = ev.eventIdentifier ?: @"";
		d[@"calendar_id"] = cid;
		d[@"uid"] = ev.calendarItemExternalIdentifier ?: ev.calendarItemIdentifier ?: @"";
		d[@"title"] = ev.title ?: @"";
		d[@"notes"] = ev.notes ?: @"";
		d[@"location"] = ev.location ?: @"";
		d[@"tz"] = nubilo_ek_tzname(ev);
		d[@"start"] = @([ev.startDate timeIntervalSince1970]);
		d[@"end"] = @([ev.endDate timeIntervalSince1970]);
		d[@"all_day"] = @(ev.allDay ? 1 : 0);
		d[@"detached"] = @(ev.isDetached ? 1 : 0);
		d[@"occurrence"] = @([ev.occurrenceDate timeIntervalSince1970]);
		nubilo_ek_fill_ics(d, ev);
		if (!ev.isDetached) {
			NSString *eid = nubilo_ek_series_id(ev.eventIdentifier ?: @"");
			EKEvent *master = eid.length ? masters[eid] : nil;
			if (!master && eid.length) {
				master = [store eventWithIdentifier:eid];
				if (master) {
					masters[eid] = master;
				}
			}
			if (!master && ev.hasRecurrenceRules) {
				master = ev;
			}
			if (master.hasRecurrenceRules) {
				d[@"id"] = master.calendarItemIdentifier ?: d[@"id"];
				d[@"event_id"] = master.eventIdentifier ?: eid;
				d[@"uid"] = master.calendarItemExternalIdentifier ?: master.calendarItemIdentifier ?: d[@"uid"];
				d[@"title"] = master.title ?: d[@"title"];
				d[@"notes"] = master.notes ?: @"";
				d[@"location"] = master.location ?: @"";
				d[@"tz"] = nubilo_ek_tzname(master);
				d[@"master_start"] = @([master.startDate timeIntervalSince1970]);
				d[@"master_end"] = @([master.endDate timeIntervalSince1970]);
				d[@"rrule"] = nubilo_ek_rrule(master.recurrenceRules.firstObject, master.allDay);
				d[@"all_day"] = @(master.allDay ? 1 : 0);
				nubilo_ek_fill_ics(d, master);
			}
		}
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

void nubilo_ek_watch_changes(void (*cb)(void)) {
	static dispatch_once_t once;
	static void (*callback)(void);
	callback = cb;
	dispatch_once(&once, ^{
		[[NSNotificationCenter defaultCenter] addObserverForName:EKEventStoreChangedNotification
			object:nubilo_ek_store() queue:nil usingBlock:^(NSNotification *note) {
				(void)note;
				if (callback) {
					callback();
				}
			}];
	});
}
