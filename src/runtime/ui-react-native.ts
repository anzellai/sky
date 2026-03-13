import React from 'react';

/**
 * React Native implementation of Sky UI components.
 * 
 * Note: These are intended to be used in a React Native environment where 
 * 'react-native' components are available.
 */

// Use global React Native components if available, otherwise just use names
// This is a common pattern for platform-agnostic libs that might be bundled.
const RN = {
  Text: 'Text',
  View: 'View',
  Pressable: 'Pressable',
  Image: 'Image',
  ScrollView: 'ScrollView',
};

export const text = (props: any) => (value: string) => 
  React.createElement(RN.Text, props, value);

export const column = (props: any) => (children: any[]) => 
  React.createElement(RN.View, { 
    ...props, 
    style: { display: 'flex', flexDirection: 'column', ...(props.style || {}) } 
  }, ...children);

export const row = (props: any) => (children: any[]) => 
  React.createElement(RN.View, { 
    ...props, 
    style: { display: 'flex', flexDirection: 'row', ...(props.style || {}) } 
  }, ...children);

export const button = (props: any) => (label: string) => 
  React.createElement(RN.Pressable, props, React.createElement(RN.Text, {}, label));

export const image = (props: any) => 
  React.createElement(RN.Image, props);

export const scrollView = (props: any) => (children: any[]) => 
  React.createElement(RN.ScrollView, props, ...children);

export const spacer = (props: any) => 
  React.createElement(RN.View, { 
    ...props, 
    style: { flex: 1, ...(props.style || {}) } 
  });
