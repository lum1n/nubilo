//go:build darwin

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

static EKWeekday nubilo_ek_weekday(NSString *s) {
	if ([s isEqualToString:@"SU"]) {
		return EKSunday;
	}
	if ([s isEqualToString:@"MO"]) {
		return EKMonday;
	}
	if ([s isEqualToString:@"TU"]) {
		return EKTuesday;
	}
	if ([s isEqualToString:@"WE"]) {
		return EKWednesday;
	}
	if ([s isEqualToString:@"TH"]) {
		return EKThursday;
	}
	if ([s isEqualToString:@"FR"]) {
		return EKFriday;
	}
	if ([s isEqualToString:@"SA"]) {
		return EKSaturday;
	}
	return EKMonday;
}

static EKRecurrenceRule *nubilo_ek_rule_from_json(NSDictionary *j) {
	if (![j isKindOfClass:[NSDictionary class]]) {
		return nil;
	}
	NSString *freqS = j[@"freq"];
	EKRecurrenceFrequency freq = EKRecurrenceFrequencyWeekly;
	if ([freqS isEqualToString:@"DAILY"]) {
		freq = EKRecurrenceFrequencyDaily;
	} else if ([freqS isEqualToString:@"WEEKLY"]) {
		freq = EKRecurrenceFrequencyWeekly;
	} else if ([freqS isEqualToString:@"MONTHLY"]) {
		freq = EKRecurrenceFrequencyMonthly;
	} else if ([freqS isEqualToString:@"YEARLY"]) {
		freq = EKRecurrenceFrequencyYearly;
	} else {
		return nil;
	}
	NSInteger interval = [j[@"interval"] integerValue];
	if (interval < 1) {
		interval = 1;
	}
	EKRecurrenceEnd *rend = nil;
	NSInteger count = [j[@"count"] integerValue];
	double until = [j[@"until"] doubleValue];
	if (count > 0) {
		rend = [EKRecurrenceEnd recurrenceEndWithOccurrenceCount:(NSUInteger)count];
	} else if (until > 0) {
		rend = [EKRecurrenceEnd recurrenceEndWithEndDate:[NSDate dateWithTimeIntervalSince1970:until]];
	}
	NSMutableArray *days = [NSMutableArray array];
	for (NSString *raw in j[@"byday"]) {
		if (![raw isKindOfClass:[NSString class]] || raw.length < 2) {
			continue;
		}
		NSInteger week = 0;
		NSString *wd = raw;
		unichar c = [raw characterAtIndex:0];
		if (c == '-' || (c >= '1' && c <= '9')) {
			NSInteger i = 0;
			if (c == '-') {
				i = 1;
			}
			while (i < (NSInteger)raw.length && [raw characterAtIndex:i] >= '0' && [raw characterAtIndex:i] <= '9') {
				i++;
			}
			week = [[raw substringToIndex:i] integerValue];
			wd = [raw substringFromIndex:i];
		}
		EKWeekday d = nubilo_ek_weekday(wd);
		if (week != 0) {
			[days addObject:[EKRecurrenceDayOfWeek dayOfWeek:d weekNumber:week]];
		} else {
			[days addObject:[EKRecurrenceDayOfWeek dayOfWeek:d]];
		}
	}
	NSArray *byday = days.count ? days : nil;
	NSArray *bymday = [j[@"bymonthday"] isKindOfClass:[NSArray class]] ? j[@"bymonthday"] : nil;
	NSArray *bymonth = [j[@"bymonth"] isKindOfClass:[NSArray class]] ? j[@"bymonth"] : nil;
	NSArray *byset = [j[@"bysetpos"] isKindOfClass:[NSArray class]] ? j[@"bysetpos"] : nil;
	NSArray *byyear = [j[@"byyearday"] isKindOfClass:[NSArray class]] ? j[@"byyearday"] : nil;
	NSArray *byweek = [j[@"byweekno"] isKindOfClass:[NSArray class]] ? j[@"byweekno"] : nil;
	return [[EKRecurrenceRule alloc] initRecurrenceWithFrequency:freq
	                                                    interval:interval
	                                               daysOfTheWeek:byday
	                                              daysOfTheMonth:bymday
	                                             monthsOfTheYear:bymonth
	                                              weeksOfTheYear:byweek
	                                               daysOfTheYear:byyear
	                                                setPositions:byset
	                                                         end:rend];
}

static EKEvent *nubilo_ek_occurrence_near(EKEventStore *store, EKCalendar *cal, EKEvent *master, NSDate *rid) {
	NSDate *from = [rid dateByAddingTimeInterval:-36 * 3600];
	NSDate *to = [rid dateByAddingTimeInterval:36 * 3600];
	NSPredicate *pred = [store predicateForEventsWithStartDate:from endDate:to calendars:@[ cal ]];
	NSString *mid = master.eventIdentifier ?: @"";
	for (EKEvent *e in [store eventsMatchingPredicate:pred]) {
		if (![e.eventIdentifier isEqualToString:mid] && ![e.eventIdentifier hasPrefix:[mid stringByAppendingString:@"/"]]) {
			continue;
		}
		if (fabs([e.occurrenceDate timeIntervalSinceDate:rid]) < 20 * 3600) {
			return e;
		}
	}
	return nil;
}

static EKEvent *nubilo_ek_as_event(EKCalendarItem *item) {
	if ([item isKindOfClass:[EKEvent class]]) {
		return (EKEvent *)item;
	}
	return nil;
}

static NSString *nubilo_ek_series_id(NSString *eventID) {
	NSRange slash = [eventID rangeOfString:@"/"];
	if (slash.location == NSNotFound || slash.location == 0) {
		return eventID;
	}
	return [eventID substringToIndex:slash.location];
}

static BOOL nubilo_ek_repeat_denied(NSError *e) {
	if (!e) {
		return NO;
	}
	NSString *d = e.localizedDescription ?: @"";
	if ([d localizedCaseInsensitiveContainsString:@"repeat field"]) {
		return YES;
	}
	return [e.domain isEqualToString:EKErrorDomain] && e.code == 28;
}

static BOOL nubilo_ek_rule_same(EKEvent *ev, EKRecurrenceRule *want) {
	EKRecurrenceRule *have = nil;
	if (ev.hasRecurrenceRules && ev.recurrenceRules.count) {
		have = ev.recurrenceRules[0];
	}
	NSString *a = nubilo_ek_rrule(have, ev.allDay) ?: @"";
	NSString *b = nubilo_ek_rrule(want, ev.allDay) ?: @"";
	return [a isEqualToString:b];
}

static void nubilo_ek_apply_extras(EKEvent *ev, NSDictionary *j);

static void nubilo_ek_apply_body(EKEvent *ev, EKCalendar *cal, NSDictionary *j) {
	ev.calendar = cal;
	ev.title = [j[@"title"] isKindOfClass:[NSString class]] ? j[@"title"] : @"";
	ev.notes = [j[@"notes"] isKindOfClass:[NSString class]] ? j[@"notes"] : @"";
	ev.location = [j[@"location"] isKindOfClass:[NSString class]] ? j[@"location"] : @"";
	double start = [j[@"start"] doubleValue];
	double end = [j[@"end"] doubleValue];
	ev.startDate = [NSDate dateWithTimeIntervalSince1970:start];
	ev.endDate = [NSDate dateWithTimeIntervalSince1970:end > start ? end : start + 3600];
	ev.allDay = [j[@"all_day"] intValue] != 0;
	nubilo_ek_apply_extras(ev, j);
}

static void nubilo_ek_apply_rule(EKEvent *ev, EKRecurrenceRule *rule) {
	if (rule) {
		ev.recurrenceRules = @[ rule ];
	} else if (ev.hasRecurrenceRules) {
		ev.recurrenceRules = @[];
	}
}

// The object EventKit will let us change RRULE on is the calendar item, not an
// expanded occurrence from eventWithIdentifier (that returns the first
// occurrence and save then fails with "The repeat field cannot be changed").
static EKEvent *nubilo_ek_writable(EKEventStore *store, EKCalendar *cal, NSString *itemID, NSString *uid) {
	EKEvent *ev = nil;
	if (uid.length > 0) {
		for (EKCalendarItem *item in [store calendarItemsWithExternalIdentifier:uid]) {
			EKEvent *cand = nubilo_ek_as_event(item);
			if (!cand || cand.isDetached) {
				continue;
			}
			if (cand.calendar && cal && ![cand.calendar.calendarIdentifier isEqualToString:cal.calendarIdentifier]) {
				continue;
			}
			ev = cand;
			break;
		}
	}
	if (!ev && itemID.length > 0) {
		ev = nubilo_ek_as_event([store calendarItemWithIdentifier:itemID]);
	}
	if (ev.isDetached) {
		EKEvent *probe = [store eventWithIdentifier:nubilo_ek_series_id(ev.eventIdentifier ?: @"")];
		if (probe.calendarItemIdentifier.length) {
			EKEvent *series = nubilo_ek_as_event([store calendarItemWithIdentifier:probe.calendarItemIdentifier]);
			if (series && !series.isDetached) {
				ev = series;
			}
		}
	}
	if (ev.isDetached) {
		return nil;
	}
	return ev;
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
		row[@"action"] = @"DISPLAY";
		row[@"desc"] = a.emailAddress.length ? a.emailAddress : @"Reminder";
		[alarms addObject:row];
	}
	if (alarms.count) {
		d[@"alarms"] = alarms;
	}
}

static void nubilo_ek_apply_extras(EKEvent *ev, NSDictionary *j) {
	NSString *url = [j[@"url"] isKindOfClass:[NSString class]] ? j[@"url"] : @"";
	ev.URL = url.length ? [NSURL URLWithString:url] : nil;
	NSString *status = [j[@"status"] isKindOfClass:[NSString class]] ? j[@"status"] : @"";
	if ([status isEqualToString:@"TENTATIVE"]) {
		ev.status = EKEventStatusTentative;
	} else if ([status isEqualToString:@"CANCELLED"]) {
		ev.status = EKEventStatusCanceled;
	} else if ([status isEqualToString:@"CONFIRMED"]) {
		ev.status = EKEventStatusConfirmed;
	}
	NSString *transp = [j[@"transp"] isKindOfClass:[NSString class]] ? j[@"transp"] : @"";
	if (ev.availability != EKEventAvailabilityNotSupported) {
		if ([transp isEqualToString:@"TRANSPARENT"]) {
			ev.availability = EKEventAvailabilityFree;
		} else if ([transp isEqualToString:@"OPAQUE"]) {
			ev.availability = EKEventAvailabilityBusy;
		}
	}
	NSArray *old = [ev.alarms copy];
	for (EKAlarm *a in old) {
		[ev removeAlarm:a];
	}
	for (NSDictionary *al in j[@"alarms"]) {
		if (![al isKindOfClass:[NSDictionary class]]) {
			continue;
		}
		EKAlarm *a = nil;
		if (al[@"abs"] && [al[@"abs"] doubleValue] > 0) {
			a = [EKAlarm alarmWithAbsoluteDate:[NSDate dateWithTimeIntervalSince1970:[al[@"abs"] doubleValue]]];
		} else if (al[@"offset"]) {
			a = [EKAlarm alarmWithRelativeOffset:[al[@"offset"] doubleValue]];
		}
		if (a) {
			[ev addAlarm:a];
		}
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

char *nubilo_ek_save_event(const char *calendar_id, const char *item_id, const char *json, char **err) {
	NSError *e = nil;
	if (!nubilo_ek_access(&e)) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"calendar access denied");
		}
		return NULL;
	}
	NSData *raw = [NSData dataWithBytes:json ? json : "{}" length:json ? strlen(json) : 2];
	id parsed = [NSJSONSerialization JSONObjectWithData:raw options:0 error:&e];
	if (![parsed isKindOfClass:[NSDictionary class]]) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"invalid event json");
		}
		return NULL;
	}
	NSDictionary *j = parsed;
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
	NSString *iid = [NSString stringWithUTF8String:item_id ? item_id : ""];
	NSString *uid = [j[@"uid"] isKindOfClass:[NSString class]] ? j[@"uid"] : @"";
	EKRecurrenceRule *rule = nubilo_ek_rule_from_json(j[@"rrule"]);
	BOOL created = NO;
	EKEvent *ev = nubilo_ek_writable(store, cal, iid, uid);
	if (!ev) {
		ev = [EKEvent eventWithEventStore:store];
		created = YES;
	}
	nubilo_ek_apply_body(ev, cal, j);
	BOOL changeRule = created || !nubilo_ek_rule_same(ev, rule);
	if (changeRule) {
		nubilo_ek_apply_rule(ev, rule);
	}
	EKSpan span = (rule || ev.hasRecurrenceRules) ? EKSpanFutureEvents : EKSpanThisEvent;
	if (![store saveEvent:ev span:span commit:YES error:&e]) {
		if (!nubilo_ek_repeat_denied(e)) {
			if (err) {
				*err = nubilo_dup(e.localizedDescription ?: @"save failed");
			}
			return NULL;
		}
		if (!created) {
			[store removeEvent:ev span:EKSpanFutureEvents commit:YES error:nil];
		}
		ev = [EKEvent eventWithEventStore:store];
		nubilo_ek_apply_body(ev, cal, j);
		nubilo_ek_apply_rule(ev, rule);
		e = nil;
		if (![store saveEvent:ev span:EKSpanThisEvent commit:YES error:&e]) {
			if (err) {
				*err = nubilo_dup(e.localizedDescription ?: @"save failed");
			}
			return NULL;
		}
	}
	for (NSNumber *ex in j[@"exdates"]) {
		if (![ex isKindOfClass:[NSNumber class]]) {
			continue;
		}
		NSDate *rid = [NSDate dateWithTimeIntervalSince1970:ex.doubleValue];
		EKEvent *occ = nubilo_ek_occurrence_near(store, cal, ev, rid);
		if (occ) {
			[store removeEvent:occ span:EKSpanThisEvent commit:YES error:nil];
		}
	}
	for (NSDictionary *ex in j[@"exceptions"]) {
		if (![ex isKindOfClass:[NSDictionary class]]) {
			continue;
		}
		NSDate *rid = [NSDate dateWithTimeIntervalSince1970:[ex[@"rid"] doubleValue]];
		EKEvent *occ = nubilo_ek_occurrence_near(store, cal, ev, rid);
		if (!occ) {
			continue;
		}
		occ.title = [ex[@"title"] isKindOfClass:[NSString class]] ? ex[@"title"] : occ.title;
		occ.notes = [ex[@"notes"] isKindOfClass:[NSString class]] ? ex[@"notes"] : occ.notes;
		occ.location = [ex[@"location"] isKindOfClass:[NSString class]] ? ex[@"location"] : occ.location;
		double es = [ex[@"start"] doubleValue];
		double ee = [ex[@"end"] doubleValue];
		if (es > 0) {
			occ.startDate = [NSDate dateWithTimeIntervalSince1970:es];
			occ.endDate = [NSDate dateWithTimeIntervalSince1970:ee > es ? ee : es + 3600];
		}
		occ.allDay = [ex[@"all_day"] intValue] != 0;
		[store saveEvent:occ span:EKSpanThisEvent commit:YES error:nil];
	}
	return nubilo_dup(ev.calendarItemIdentifier ?: @"");
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

static int nubilo_ek_reminder_access(NSError **outErr) {
	EKEventStore *store = nubilo_ek_store();
	EKAuthorizationStatus st = [EKEventStore authorizationStatusForEntityType:EKEntityTypeReminder];
	if (st == EKAuthorizationStatusAuthorized) {
		return 1;
	}
	__block BOOL ok = NO;
	__block NSError *err = nil;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	[store requestAccessToEntityType:EKEntityTypeReminder completion:^(BOOL granted, NSError *e) {
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

static NSDate *nubilo_ek_comp_date(NSDateComponents *comp, BOOL *allDayOut) {
	if (!comp) {
		if (allDayOut) {
			*allDayOut = NO;
		}
		return nil;
	}
	NSCalendar *cal = [NSCalendar currentCalendar];
	BOOL allDay = (comp.hour == NSDateComponentUndefined && comp.minute == NSDateComponentUndefined && comp.second == NSDateComponentUndefined);
	if (allDayOut) {
		*allDayOut = allDay;
	}
	if (allDay) {
		NSDateComponents *d = [comp copy];
		d.hour = 0;
		d.minute = 0;
		d.second = 0;
		return [cal dateFromComponents:d];
	}
	return [cal dateFromComponents:comp];
}

static NSDateComponents *nubilo_ek_date_comp(NSDate *date, BOOL allDay) {
	if (!date) {
		return nil;
	}
	NSCalendar *cal = [NSCalendar currentCalendar];
	NSCalendarUnit units = NSCalendarUnitYear | NSCalendarUnitMonth | NSCalendarUnitDay | NSCalendarUnitHour | NSCalendarUnitMinute | NSCalendarUnitSecond;
	NSDateComponents *c = [cal components:units fromDate:date];
	if (allDay) {
		c.hour = NSDateComponentUndefined;
		c.minute = NSDateComponentUndefined;
		c.second = NSDateComponentUndefined;
	}
	return c;
}

static void nubilo_ek_fill_reminder_alarms(NSMutableDictionary *d, EKReminder *rem) {
	NSMutableArray *alarms = [NSMutableArray array];
	for (EKAlarm *a in rem.alarms) {
		NSMutableDictionary *row = [NSMutableDictionary dictionary];
		if (a.absoluteDate) {
			row[@"abs"] = @([a.absoluteDate timeIntervalSince1970]);
		} else {
			row[@"offset"] = @(a.relativeOffset);
		}
		row[@"action"] = @"DISPLAY";
		row[@"desc"] = @"Reminder";
		[alarms addObject:row];
	}
	if (alarms.count) {
		d[@"alarms"] = alarms;
	}
}

static void nubilo_ek_apply_reminder_alarms(EKReminder *rem, NSDictionary *j) {
	NSArray *old = [rem.alarms copy];
	for (EKAlarm *a in old) {
		[rem removeAlarm:a];
	}
	for (NSDictionary *al in j[@"alarms"]) {
		if (![al isKindOfClass:[NSDictionary class]]) {
			continue;
		}
		EKAlarm *a = nil;
		if (al[@"abs"] && [al[@"abs"] doubleValue] > 0) {
			a = [EKAlarm alarmWithAbsoluteDate:[NSDate dateWithTimeIntervalSince1970:[al[@"abs"] doubleValue]]];
		} else if (al[@"offset"]) {
			a = [EKAlarm alarmWithRelativeOffset:[al[@"offset"] doubleValue]];
		}
		if (a) {
			[rem addAlarm:a];
		}
	}
}

char *nubilo_ek_list_reminder_lists(char **err) {
	NSError *e = nil;
	if (!nubilo_ek_reminder_access(&e)) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"reminders access denied");
		}
		return NULL;
	}
	NSMutableArray *out = [NSMutableArray array];
	for (EKCalendar *cal in [nubilo_ek_store() calendarsForEntityType:EKEntityTypeReminder]) {
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

char *nubilo_ek_list_reminders(const char *calendar_id, double start, double end, char **err) {
	NSError *e = nil;
	if (!nubilo_ek_reminder_access(&e)) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"reminders access denied");
		}
		return NULL;
	}
	EKEventStore *store = nubilo_ek_store();
	NSString *cid = [NSString stringWithUTF8String:calendar_id ? calendar_id : ""];
	EKCalendar *want = nil;
	for (EKCalendar *cal in [store calendarsForEntityType:EKEntityTypeReminder]) {
		if ([cal.calendarIdentifier isEqualToString:cid]) {
			want = cal;
			break;
		}
	}
	if (!want) {
		if (err) {
			*err = strdup("reminder list not found");
		}
		return NULL;
	}
	NSPredicate *pred = [store predicateForRemindersInCalendars:@[ want ]];
	__block NSArray *reminders = nil;
	dispatch_semaphore_t sem = dispatch_semaphore_create(0);
	[store fetchRemindersMatchingPredicate:pred completion:^(NSArray *rems) {
		reminders = rems ?: @[];
		dispatch_semaphore_signal(sem);
	}];
	while (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 50 * NSEC_PER_MSEC))) {
		[[NSRunLoop currentRunLoop] runMode:NSDefaultRunLoopMode beforeDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
	}
	NSMutableArray *out = [NSMutableArray array];
	for (EKReminder *rem in reminders) {
		if (![rem isKindOfClass:[EKReminder class]] || rem.calendarItemIdentifier.length == 0) {
			continue;
		}
		BOOL dueAllDay = NO;
		NSDate *due = nubilo_ek_comp_date(rem.dueDateComponents, &dueAllDay);
		BOOL startAllDay = NO;
		NSDate *startDate = nubilo_ek_comp_date(rem.startDateComponents, &startAllDay);
		NSMutableDictionary *d = [NSMutableDictionary dictionary];
		d[@"id"] = rem.calendarItemIdentifier;
		d[@"list_id"] = cid;
		d[@"uid"] = rem.calendarItemExternalIdentifier.length ? rem.calendarItemExternalIdentifier : rem.calendarItemIdentifier;
		d[@"title"] = rem.title ?: @"";
		d[@"notes"] = rem.notes ?: @"";
		d[@"url"] = rem.URL.absoluteString ?: @"";
		d[@"priority"] = @(rem.priority);
		d[@"all_day"] = @((dueAllDay || startAllDay) ? 1 : 0);
		if (startDate) {
			d[@"start"] = @([startDate timeIntervalSince1970]);
		}
		if (due) {
			d[@"due"] = @([due timeIntervalSince1970]);
		}
		if (rem.completed) {
			d[@"status"] = @"COMPLETED";
			d[@"percent"] = @100;
			if (rem.completionDate) {
				d[@"completed"] = @([rem.completionDate timeIntervalSince1970]);
			}
		} else {
			d[@"status"] = @"NEEDS-ACTION";
		}
		if (rem.recurrenceRules.count) {
			NSString *rr = nubilo_ek_rrule(rem.recurrenceRules.firstObject, dueAllDay || startAllDay);
			if (rr.length) {
				d[@"rrule"] = rr;
			}
		}
		nubilo_ek_fill_reminder_alarms(d, rem);
		[out addObject:d];
	}
	(void)start;
	(void)end;
	NSData *data = [NSJSONSerialization dataWithJSONObject:out options:0 error:&e];
	if (!data) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"json");
		}
		return NULL;
	}
	return nubilo_dup([[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding]);
}

char *nubilo_ek_save_reminder(const char *calendar_id, const char *item_id, const char *json, char **err) {
	NSError *e = nil;
	if (!nubilo_ek_reminder_access(&e)) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"reminders access denied");
		}
		return NULL;
	}
	NSData *raw = [NSData dataWithBytes:json ? json : "{}" length:json ? strlen(json) : 2];
	id parsed = [NSJSONSerialization JSONObjectWithData:raw options:0 error:&e];
	if (![parsed isKindOfClass:[NSDictionary class]]) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"invalid reminder json");
		}
		return NULL;
	}
	NSDictionary *j = parsed;
	EKEventStore *store = nubilo_ek_store();
	NSString *cid = [NSString stringWithUTF8String:calendar_id ? calendar_id : ""];
	EKCalendar *cal = nil;
	for (EKCalendar *c in [store calendarsForEntityType:EKEntityTypeReminder]) {
		if ([c.calendarIdentifier isEqualToString:cid]) {
			cal = c;
			break;
		}
	}
	if (!cal) {
		if (err) {
			*err = strdup("reminder list not found");
		}
		return NULL;
	}
	NSString *iid = [NSString stringWithUTF8String:item_id ? item_id : ""];
	EKReminder *rem = nil;
	if (iid.length) {
		EKCalendarItem *item = [store calendarItemWithIdentifier:iid];
		if ([item isKindOfClass:[EKReminder class]]) {
			rem = (EKReminder *)item;
		}
	}
	if (!rem) {
		rem = [EKReminder reminderWithEventStore:store];
	}
	rem.calendar = cal;
	rem.title = [j[@"title"] isKindOfClass:[NSString class]] ? j[@"title"] : @"";
	rem.notes = [j[@"notes"] isKindOfClass:[NSString class]] ? j[@"notes"] : nil;
	NSString *url = [j[@"url"] isKindOfClass:[NSString class]] ? j[@"url"] : @"";
	rem.URL = url.length ? [NSURL URLWithString:url] : nil;
	rem.priority = [j[@"priority"] intValue];
	BOOL allDay = [j[@"all_day"] intValue] != 0;
	double due = [j[@"due"] doubleValue];
	double startSec = [j[@"start"] doubleValue];
	rem.dueDateComponents = due > 0 ? nubilo_ek_date_comp([NSDate dateWithTimeIntervalSince1970:due], allDay) : nil;
	rem.startDateComponents = startSec > 0 ? nubilo_ek_date_comp([NSDate dateWithTimeIntervalSince1970:startSec], allDay) : nil;
	NSString *status = [j[@"status"] isKindOfClass:[NSString class]] ? j[@"status"] : @"";
	double completed = [j[@"completed"] doubleValue];
	BOOL done = completed > 0 || [status isEqualToString:@"COMPLETED"];
	rem.completed = done;
	if (done && completed > 0) {
		rem.completionDate = [NSDate dateWithTimeIntervalSince1970:completed];
	} else if (!done) {
		rem.completionDate = nil;
	}
	EKRecurrenceRule *rule = nubilo_ek_rule_from_json(j[@"rrule"]);
	if (rule) {
		rem.recurrenceRules = @[ rule ];
	} else {
		rem.recurrenceRules = nil;
	}
	nubilo_ek_apply_reminder_alarms(rem, j);
	if (![store saveReminder:rem commit:YES error:&e]) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"save reminder failed");
		}
		return NULL;
	}
	return nubilo_dup(rem.calendarItemIdentifier ?: @"");
}

int nubilo_ek_delete_reminder(const char *item_id, char **err) {
	NSError *e = nil;
	if (!nubilo_ek_reminder_access(&e)) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"reminders access denied");
		}
		return 0;
	}
	NSString *iid = [NSString stringWithUTF8String:item_id ? item_id : ""];
	EKCalendarItem *item = [nubilo_ek_store() calendarItemWithIdentifier:iid];
	if (![item isKindOfClass:[EKReminder class]]) {
		return 1;
	}
	if (![nubilo_ek_store() removeReminder:(EKReminder *)item commit:YES error:&e]) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"delete reminder failed");
		}
		return 0;
	}
	return 1;
}
