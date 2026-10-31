package helper

import "time"

// DateBuilder mirrors org.traccar.helper.DateBuilder: a mutable UTC date/time
// where each component can be assigned independently. Unassigned components
// default to the epoch, exactly like the Java implementation which starts from
// a cleared calendar set to zero milliseconds.
type DateBuilder struct {
	year        int
	month       time.Month
	day         int
	hour        int
	minute      int
	second      int
	millisecond int
}

// NewDateBuilder creates a builder initialised to 1970-01-01T00:00:00Z.
func NewDateBuilder() *DateBuilder {
	return &DateBuilder{year: 1970, month: time.January, day: 1}
}

// NewDateBuilderFrom creates a builder initialised from an existing time.
func NewDateBuilderFrom(t time.Time) *DateBuilder {
	u := t.UTC()
	return &DateBuilder{
		year:        u.Year(),
		month:       u.Month(),
		day:         u.Day(),
		hour:        u.Hour(),
		minute:      u.Minute(),
		second:      u.Second(),
		millisecond: u.Nanosecond() / int(time.Millisecond),
	}
}

// SetYear sets the year, expanding two digit years into the 2000s.
func (d *DateBuilder) SetYear(year int) *DateBuilder {
	if year < 100 {
		year += 2000
	}
	d.year = year
	return d
}

// SetMonth sets a one based month.
func (d *DateBuilder) SetMonth(month int) *DateBuilder {
	d.month = time.Month(month)
	return d
}

// SetDay sets the day of month.
func (d *DateBuilder) SetDay(day int) *DateBuilder {
	d.day = day
	return d
}

// SetDate sets year, month and day.
func (d *DateBuilder) SetDate(year, month, day int) *DateBuilder {
	return d.SetYear(year).SetMonth(month).SetDay(day)
}

// SetDateReverse sets the date from day, month, year order.
func (d *DateBuilder) SetDateReverse(day, month, year int) *DateBuilder {
	return d.SetDate(year, month, day)
}

// SetCurrentDate copies the current UTC date.
func (d *DateBuilder) SetCurrentDate() *DateBuilder {
	now := time.Now().UTC()
	return d.SetDate(now.Year(), int(now.Month()), now.Day())
}

// SetHour sets the hour of day.
func (d *DateBuilder) SetHour(hour int) *DateBuilder {
	d.hour = hour
	return d
}

// SetMinute sets the minute.
func (d *DateBuilder) SetMinute(minute int) *DateBuilder {
	d.minute = minute
	return d
}

// AddMinute adds minutes to the current value.
func (d *DateBuilder) AddMinute(minute int) *DateBuilder {
	t := d.Date().Add(time.Duration(minute) * time.Minute)
	*d = *NewDateBuilderFrom(t)
	return d
}

// SetSecond sets the second.
func (d *DateBuilder) SetSecond(second int) *DateBuilder {
	d.second = second
	return d
}

// AddSeconds adds seconds to the current value.
func (d *DateBuilder) AddSeconds(seconds int64) *DateBuilder {
	t := d.Date().Add(time.Duration(seconds) * time.Second)
	*d = *NewDateBuilderFrom(t)
	return d
}

// SetMillis sets the millisecond.
func (d *DateBuilder) SetMillis(millis int) *DateBuilder {
	d.millisecond = millis
	return d
}

// AddMillis adds milliseconds to the current value.
func (d *DateBuilder) AddMillis(millis int64) *DateBuilder {
	t := d.Date().Add(time.Duration(millis) * time.Millisecond)
	*d = *NewDateBuilderFrom(t)
	return d
}

// SetTime sets hour, minute and second.
func (d *DateBuilder) SetTime(hour, minute, second int) *DateBuilder {
	return d.SetHour(hour).SetMinute(minute).SetSecond(second)
}

// SetTimeReverse sets the time from second, minute, hour order.
func (d *DateBuilder) SetTimeReverse(second, minute, hour int) *DateBuilder {
	return d.SetHour(hour).SetMinute(minute).SetSecond(second)
}

// SetTimeMillis sets hour, minute, second and millisecond.
func (d *DateBuilder) SetTimeMillis(hour, minute, second, millis int) *DateBuilder {
	return d.SetHour(hour).SetMinute(minute).SetSecond(second).SetMillis(millis)
}

// Date resolves the builder into a UTC time value.
func (d *DateBuilder) Date() time.Time {
	return time.Date(d.year, d.month, d.day, d.hour, d.minute, d.second,
		d.millisecond*int(time.Millisecond), time.UTC)
}
