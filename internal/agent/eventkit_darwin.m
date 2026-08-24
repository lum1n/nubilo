//go:build darwin

#import "eventkit_darwin.h"
#import <EventKit/EventKit.h>
#import <Foundation/Foundation.h>
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
		d[@"event_id"] = ev.eventIdentifier ?: @"";
		d[@"calendar_id"] = cid;
		d[@"uid"] = ev.calendarItemExternalIdentifier ?: ev.calendarItemIdentifier ?: @"";
		d[@"title"] = ev.title ?: @"";
		d[@"notes"] = ev.notes ?: @"";
		d[@"location"] = ev.location ?: @"";
		d[@"start"] = @([ev.startDate timeIntervalSince1970]);
		d[@"end"] = @([ev.endDate timeIntervalSince1970]);
		d[@"all_day"] = @(ev.allDay ? 1 : 0);
		d[@"detached"] = @(ev.isDetached ? 1 : 0);
		d[@"occurrence"] = @([ev.occurrenceDate timeIntervalSince1970]);
		if (ev.hasRecurrenceRules && !ev.isDetached) {
			EKEvent *master = [store eventWithIdentifier:ev.eventIdentifier];
			if (!master) {
				master = ev;
			}
			d[@"uid"] = master.calendarItemExternalIdentifier ?: master.calendarItemIdentifier ?: d[@"uid"];
			d[@"title"] = master.title ?: d[@"title"];
			d[@"notes"] = master.notes ?: @"";
			d[@"location"] = master.location ?: @"";
			d[@"master_start"] = @([master.startDate timeIntervalSince1970]);
			d[@"master_end"] = @([master.endDate timeIntervalSince1970]);
			d[@"rrule"] = nubilo_ek_rrule(master.recurrenceRules.firstObject, master.allDay);
			d[@"all_day"] = @(master.allDay ? 1 : 0);
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
	EKEvent *ev = nil;
	NSString *iid = [NSString stringWithUTF8String:item_id ? item_id : ""];
	NSString *uid = [j[@"uid"] isKindOfClass:[NSString class]] ? j[@"uid"] : @"";
	if (iid.length > 0) {
		EKCalendarItem *item = [store calendarItemWithIdentifier:iid];
		if ([item isKindOfClass:[EKEvent class]]) {
			ev = (EKEvent *)item;
		}
	}
	if (!ev && uid.length > 0) {
		for (EKCalendarItem *item in [store calendarItemsWithExternalIdentifier:uid]) {
			if (![item isKindOfClass:[EKEvent class]]) {
				continue;
			}
			EKEvent *cand = (EKEvent *)item;
			if (cand.calendar && ![cand.calendar.calendarIdentifier isEqualToString:cid]) {
				continue;
			}
			if (cand.isDetached) {
				continue;
			}
			ev = cand;
			break;
		}
	}
	if (ev) {
		EKEvent *master = [store eventWithIdentifier:ev.eventIdentifier];
		if (master) {
			ev = master;
		}
	} else {
		ev = [EKEvent eventWithEventStore:store];
	}
	ev.calendar = cal;
	ev.title = [j[@"title"] isKindOfClass:[NSString class]] ? j[@"title"] : @"";
	ev.notes = [j[@"notes"] isKindOfClass:[NSString class]] ? j[@"notes"] : @"";
	ev.location = [j[@"location"] isKindOfClass:[NSString class]] ? j[@"location"] : @"";
	double start = [j[@"start"] doubleValue];
	double end = [j[@"end"] doubleValue];
	ev.startDate = [NSDate dateWithTimeIntervalSince1970:start];
	ev.endDate = [NSDate dateWithTimeIntervalSince1970:end > start ? end : start + 3600];
	ev.allDay = [j[@"all_day"] intValue] != 0;
	BOOL hadRules = ev.hasRecurrenceRules;
	EKRecurrenceRule *rule = nubilo_ek_rule_from_json(j[@"rrule"]);
	if (rule) {
		ev.recurrenceRules = @[ rule ];
	} else {
		ev.recurrenceRules = @[];
	}
	EKSpan span = EKSpanThisEvent;
	if (iid.length > 0 && (rule || hadRules)) {
		span = EKSpanFutureEvents;
	}
	if (![store saveEvent:ev span:span commit:YES error:&e]) {
		if (err) {
			*err = nubilo_dup(e.localizedDescription ?: @"save failed");
		}
		return NULL;
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
	EKEvent *fresh = [store eventWithIdentifier:ev.eventIdentifier];
	NSString *outID = fresh.calendarItemIdentifier.length ? fresh.calendarItemIdentifier : ev.calendarItemIdentifier;
	return nubilo_dup(outID ?: @"");
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
