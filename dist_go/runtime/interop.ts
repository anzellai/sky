/**
 * Sky Interop Runtime
 */

// Basic Result constructors used by runDecoder
const Ok = (a: any) => ({ $: 'Ok', values: [a] });
const Err = (e: any) => ({ $: 'Err', values: [e] });

export const succeed = (a: any) => ({
    decode: (v: any) => Ok(a)
});

export const fail = (msg: string) => ({
    decode: (v: any) => Err({ message: msg })
});

export const decodeString = {
    decode: (v: any) => typeof v === 'string' ? Ok(v) : Err({ message: `Expected string, got ${typeof v}` })
};

export const decodeFloat = {
    decode: (v: any) => typeof v === 'number' ? Ok(v) : Err({ message: `Expected number, got ${typeof v}` })
};

export const decodeBool = {
    decode: (v: any) => typeof v === 'boolean' ? Ok(v) : Err({ message: `Expected boolean, got ${typeof v}` })
};

export const decodeField = (field: string) => (decoder: any) => ({
    decode: (v: any) => {
        if (v && typeof v === 'object' && field in v) {
            return decoder.decode(v[field]);
        }
        return Err({ message: `Field ${field} not found` });
    }
});

export const decodeList = (decoder: any) => ({
    decode: (v: any) => {
        if (Array.isArray(v)) {
            const result = [];
            for (const item of v) {
                const res = decoder.decode(item);
                if (res.$ === 'Err') return res;
                result.push(res.values[0]);
            }
            return Ok(result);
        }
        return Err({ message: `Expected array, got ${typeof v}` });
    }
});

export const decodeMaybe = (decoder: any) => ({
    decode: (v: any) => {
        if (v === null || v === undefined) {
            return Ok({ $: 'Nothing', values: [] });
        }
        const res = decoder.decode(v);
        if (res.$ === 'Err') return res;
        return Ok({ $: 'Just', values: [res.values[0]] });
    }
});

export const runDecoder = (decoder: any) => (value: any) => {
    return decoder.decode(value);
};

// Encoders
export const string = (v: string) => v;
export const float = (v: number) => v;
export const bool = (v: boolean) => v;
export const null_ = null;
export const list = (v: any[]) => v;
export const object = (fields: [string, any][]) => {
    const obj: any = {};
    for (const [k, v] of fields) {
        obj[k] = v;
    }
    return obj;
};
